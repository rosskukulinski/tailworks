package compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Run is what a probe gets: the deployed workload, reached over the
// tailnet from the suite's client node.
type Run struct {
	P *Project
	// Base is http://<workload IP> (port 80, the workload node).
	Base string
	HC   *http.Client
	// Tw runs the tw CLI against the run's stack.
	Tw func(args ...string) (stdout, stderr string, err error)
	// Steps records what the probe checked (the results file).
	Steps []string

	// Hdr is sent with every request after it is set (key, value pairs).
	Hdr []string
	// WaitLogFn overrides WaitLog (tw dev: the dev process's own output).
	WaitLogFn func(s string, timeout time.Duration) (string, error)
}

// Res is one HTTP response.
type Res struct {
	Method, Path string
	Code         int
	Header       http.Header
	Body         string
	Err          error
}

func (r Res) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s %s: %v", r.Method, r.Path, r.Err)
	}
	return fmt.Sprintf("%s %s: %d %q", r.Method, r.Path, r.Code, tail(strings.TrimSpace(r.Body), 300))
}

// Want checks the status and that the body contains every string.
func (r Res) Want(code int, contains ...string) error {
	if r.Err != nil || r.Code != code {
		return fmt.Errorf("want %d: %s", code, r)
	}
	for _, c := range contains {
		if !strings.Contains(r.Body, c) {
			return fmt.Errorf("want body to contain %q: %s", c, r)
		}
	}
	return nil
}

// JSON decodes the body into v.
func (r Res) JSON(v any) error {
	if r.Err != nil {
		return r.Err
	}
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		return fmt.Errorf("%s: not JSON: %v", r, err)
	}
	return nil
}

// Do makes one request; hdr is key, value pairs. Redirects are not
// followed (probes check them).
func (r *Run) Do(method, path string, body []byte, hdr ...string) Res {
	res := Res{Method: method, Path: path}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, r.Base+path, rd)
	if err != nil {
		res.Err = err
		return res
	}
	hdr = append(append([]string{}, r.Hdr...), hdr...)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	hc := *r.HC
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	res.Code, res.Header, res.Body = resp.StatusCode, resp.Header, string(b)
	r.Steps = append(r.Steps, fmt.Sprintf("%s %s → %d", method, path, res.Code))
	return res
}

// Get is Do("GET", …).
func (r *Run) Get(path string, hdr ...string) Res { return r.Do("GET", path, nil, hdr...) }

// PostJSON posts v as JSON.
func (r *Run) PostJSON(path string, v any, hdr ...string) Res {
	b, _ := json.Marshal(v)
	return r.Do("POST", path, b, append([]string{"content-type", "application/json"}, hdr...)...)
}

// PostForm posts an urlencoded form.
func (r *Run) PostForm(path, form string, hdr ...string) Res {
	return r.Do("POST", path, []byte(form), append([]string{"content-type", "application/x-www-form-urlencoded"}, hdr...)...)
}

// Note records a step the probe wants in the results.
func (r *Run) Note(f string, a ...any) { r.Steps = append(r.Steps, fmt.Sprintf(f, a...)) }

// Eventually retries f until it returns nil or timeout passes.
func Eventually(timeout time.Duration, f func() error) error {
	dl := time.Now().Add(timeout)
	for {
		err := f()
		if err == nil || time.Now().After(dl) {
			return err
		}
		time.Sleep(time.Second)
	}
}

// WS opens a WebSocket to path over the tailnet.
func (r *Run) WS(path string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(r.Base, "http") + path
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: &http.Client{Transport: r.HC.Transport}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return nil, fmt.Errorf("websocket %s: %d %v", path, code, err)
	}
	r.Note("WS %s → 101", path)
	return c, nil
}

// WSRead reads one text message.
func WSRead(c *websocket.Conn, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, b, err := c.Read(ctx)
	return string(b), err
}

// WSWrite sends one text message.
func WSWrite(c *websocket.Conn, s string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, []byte(s))
}

// MCP is a Streamable HTTP MCP client session.
type MCP struct {
	r    *Run
	path string
	sid  string
	id   int
}

// MCPConnect runs initialize (+ notifications/initialized) on path and
// returns the server's serverInfo name.
func (r *Run) MCPConnect(path string) (*MCP, string, error) {
	return r.MCPConnectWith(path, map[string]any{})
}

// MCPConnectWith is MCPConnect with the client's capabilities.
func (r *Run) MCPConnectWith(path string, caps map[string]any) (*MCP, string, error) {
	m := &MCP{r: r, path: path}
	res, err := m.Call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    caps,
		"clientInfo":      map[string]any{"name": "tw-compat", "version": "1"},
	})
	if err != nil {
		return nil, "", err
	}
	var init struct {
		ServerInfo struct{ Name string } `json:"serverInfo"`
	}
	json.Unmarshal(res, &init)
	m.notify("notifications/initialized")
	return m, init.ServerInfo.Name, nil
}

func (m *MCP) post(msg map[string]any) Res {
	hdr := []string{"accept", "application/json, text/event-stream", "mcp-protocol-version", "2025-06-18"}
	if m.sid != "" {
		hdr = append(hdr, "mcp-session-id", m.sid)
	}
	res := m.r.PostJSON(m.path, msg, hdr...)
	if s := res.Header.Get("mcp-session-id"); s != "" {
		m.sid = s
	}
	return res
}

func (m *MCP) notify(method string) {
	m.post(map[string]any{"jsonrpc": "2.0", "method": method})
}

// Call makes one JSON-RPC request and returns its result (JSON or the
// first SSE data event that answers it).
func (m *MCP) Call(method string, params any) (json.RawMessage, error) {
	m.id++
	res := m.post(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": method, "params": params})
	if res.Err != nil || res.Code != 200 {
		return nil, fmt.Errorf("mcp %s: %s", method, res)
	}
	body := res.Body
	if strings.HasPrefix(res.Header.Get("content-type"), "text/event-stream") {
		body = ""
		sc := bufio.NewScanner(strings.NewReader(res.Body))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok && strings.Contains(d, `"id"`) {
				body = strings.TrimSpace(d)
				break
			}
		}
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &rpc); err != nil {
		return nil, fmt.Errorf("mcp %s: %v: %s", method, err, res)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp %s: error %d %s", method, rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result, nil
}

// Tool calls a tool and returns its text content.
func (m *MCP) Tool(name string, args map[string]any) (string, error) {
	res, err := m.Call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct{ Type, Text string } `json:"content"`
		IsError bool                          `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	var s []string
	for _, c := range out.Content {
		s = append(s, c.Text)
	}
	if out.IsError {
		return "", fmt.Errorf("tool %s: %s", name, strings.Join(s, " "))
	}
	m.r.Note("MCP tools/call %s → %q", name, strings.Join(s, " "))
	return strings.Join(s, " "), nil
}

// WaitLog waits until the workload's console output (tw logs --console)
// contains s.
func (r *Run) WaitLog(s string, timeout time.Duration) (string, error) {
	if r.WaitLogFn != nil {
		return r.WaitLogFn(s, timeout)
	}
	var last string
	err := Eventually(timeout, func() error {
		out, se, err := r.Tw("logs", r.P.ID, "--console", "-n", "500")
		if err != nil {
			return fmt.Errorf("tw logs: %v %s", err, se)
		}
		last = out
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, s) {
				r.Note("log line %q", tail(strings.TrimSpace(l), 200))
				last = l
				return nil
			}
		}
		return fmt.Errorf("no log line with %q after %v; last output:\n%s", s, timeout, tail(out, 1500))
	})
	return last, err
}
