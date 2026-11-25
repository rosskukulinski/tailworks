package compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// AgentWS is a WebSocket to an Agents SDK agent (what `useAgent` opens):
// it keeps what the agent sent, and makes @callable RPC calls.
type AgentWS struct {
	c    *websocket.Conn
	r    *Run
	Seen []string
	id   int
}

// Agent connects to an agent at path (/agents/<kebab-class>/<name>).
func (r *Run) Agent(path string) (*AgentWS, error) {
	c, err := r.WS(path)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(8 << 20)
	return &AgentWS{c: c, r: r}, nil
}

func (a *AgentWS) Close() { a.c.CloseNow() }

// Send writes one text message.
func (a *AgentWS) Send(s string) error { return WSWrite(a.c, s) }

func (a *AgentWS) seenTail() string {
	return fmt.Sprintf("got %d messages: %s", len(a.Seen), tail(strings.Join(a.Seen, " ⏎ "), 900))
}

// Wait returns the first message (already seen or new) that ok accepts.
func (a *AgentWS) Wait(timeout time.Duration, ok func(string) bool) (string, error) {
	for _, m := range a.Seen {
		if ok(m) {
			return m, nil
		}
	}
	return a.waitNew(timeout, ok)
}

// waitNew reads until a new message is accepted by ok.
func (a *AgentWS) waitNew(timeout time.Duration, ok func(string) bool) (string, error) {
	dl := time.Now().Add(timeout)
	for {
		left := time.Until(dl)
		if left <= 0 {
			return "", fmt.Errorf("no matching WebSocket message in %v; %s", timeout, a.seenTail())
		}
		m, err := WSRead(a.c, left)
		if err != nil {
			return "", fmt.Errorf("WebSocket read: %v; %s", err, a.seenTail())
		}
		a.Seen = append(a.Seen, m)
		if ok(m) {
			return m, nil
		}
	}
}

// Forget drops the messages seen so far (so Wait matches only new ones).
func (a *AgentWS) Forget() { a.Seen = nil }

type rpcMsg struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Done    *bool           `json:"done"`
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
}

// RPC calls a @callable method and returns its result.
func (a *AgentWS) RPC(method string, args ...any) (json.RawMessage, error) {
	res, _, err := a.RPCStream(method, args...)
	return res, err
}

// RPCStream calls a @callable method; chunks are what a streaming method
// sent before its final result.
func (a *AgentWS) RPCStream(method string, args ...any) (json.RawMessage, []string, error) {
	a.id++
	id := fmt.Sprintf("compat-%d", a.id)
	if args == nil {
		args = []any{}
	}
	b, _ := json.Marshal(map[string]any{"type": "rpc", "id": id, "method": method, "args": args})
	if err := WSWrite(a.c, string(b)); err != nil {
		return nil, nil, err
	}
	var chunks []string
	var out rpcMsg
	_, err := a.waitNew(60*time.Second, func(m string) bool {
		var x rpcMsg
		if json.Unmarshal([]byte(m), &x) != nil || x.Type != "rpc" || x.ID != id {
			return false
		}
		if x.Success && x.Done != nil && !*x.Done {
			chunks = append(chunks, string(x.Result))
			return false
		}
		out = x
		return true
	})
	if err != nil {
		return nil, chunks, fmt.Errorf("rpc %s: %v", method, err)
	}
	if !out.Success {
		return nil, chunks, fmt.Errorf("rpc %s: %s", method, out.Error)
	}
	a.r.Note("RPC %s → %s", method, tail(string(out.Result), 160))
	return out.Result, chunks, nil
}

// --- MCP elicitation: a request the server makes during a tool call ---

// ToolWithElicitation calls a tool and, when the server asks
// (elicitation/create on the call's SSE stream), answers with answer and
// keeps reading until the tool's result. It returns the result text and
// the elicitation message.
func (m *MCP) ToolWithElicitation(name string, args, answer map[string]any) (text, asked string, err error) {
	m.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", m.r.Base+m.path, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	req.Header.Set("mcp-protocol-version", "2025-06-18")
	req.Header.Set("mcp-session-id", m.sid)
	hc := &http.Client{Transport: m.r.HC.Transport}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("content-type"), "text/event-stream") {
		return "", "", fmt.Errorf("tools/call %s: %d %s", name, resp.StatusCode, resp.Header.Get("content-type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		d, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Message string `json:"message"`
			} `json:"params"`
			Result *struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(d)), &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "elicitation/create":
			asked = msg.Params.Message
			m.r.Note("MCP elicitation/create %q → accept %v", asked, answer)
			res := m.post(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"action": "accept", "content": answer}})
			if res.Err != nil || res.Code >= 300 {
				return "", asked, fmt.Errorf("answering the elicitation: %s", res)
			}
		case msg.Result != nil:
			var s []string
			for _, c := range msg.Result.Content {
				s = append(s, c.Text)
			}
			m.r.Note("MCP tools/call %s → %q", name, strings.Join(s, " "))
			return strings.Join(s, " "), asked, nil
		case msg.Error != nil:
			return "", asked, fmt.Errorf("tools/call %s: %s", name, msg.Error)
		}
	}
	return "", asked, fmt.Errorf("tools/call %s: the stream ended without a result (%v)", name, sc.Err())
}
