package compat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// probeElicitation: the stateful (2025) elicitation example. The tool asks
// the client for an amount in the middle of the call; the counter is the
// agent's state.
func probeElicitation(r *Run) error {
	m, name, err := r.MCPConnectWith("/mcp", map[string]any{"elicitation": map[string]any{}})
	if err != nil {
		return err
	}
	if name != "legacy-elicitation-demo" {
		return fmt.Errorf("serverInfo.name %q", name)
	}
	if out, err := m.Tool("increase-counter", map[string]any{"confirm": false}); err != nil || out != "Counter increase cancelled." {
		return fmt.Errorf("increase-counter confirm=false: %q %v", out, err)
	}
	out, asked, err := m.ToolWithElicitation("increase-counter", map[string]any{"confirm": true}, map[string]any{"amount": 5})
	if err != nil {
		return err
	}
	if !strings.Contains(asked, "By how much") || out != "Counter increased by 5, current value is 5" {
		return fmt.Errorf("increase-counter: asked %q, got %q", asked, out)
	}
	// The counter is Durable Object state: a second call adds to it.
	out, _, err = m.ToolWithElicitation("increase-counter", map[string]any{"confirm": true}, map[string]any{"amount": 2})
	if err != nil || out != "Counter increased by 2, current value is 7" {
		return fmt.Errorf("second increase-counter: %q %v", out, err)
	}
	return nil
}

// probeMRTR: the stateless (MCP 2026-07-28) elicitation example. Each
// round is one request; the server's signed requestState carries the tool
// call between them (the key is the MRTR_REQUEST_STATE_KEY secret).
func probeMRTR(r *Run) error {
	// A 2025 client is refused (legacy: "reject").
	old := r.PostJSON("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "tw-compat", "version": "1"}}},
		"accept", "application/json, text/event-stream", "mcp-protocol-version", "2025-06-18")
	if old.Code != 400 || !strings.Contains(old.Body, "Unsupported protocol version") {
		return fmt.Errorf("a 2025-06-18 initialize: want 400 Unsupported protocol version, got %s", old)
	}
	const ver = "2026-07-28"
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    ver,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"elicitation": map[string]any{}},
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "tw-compat", "version": "1"},
	}
	id := 1
	type result struct {
		ResultType    string `json:"resultType"`
		RequestState  string `json:"requestState"`
		InputRequests map[string]struct {
			Params struct{ Message string }
		} `json:"inputRequests"`
		Content []struct{ Text string } `json:"content"`
	}
	call := func(state string, responses map[string]any) (*result, error) {
		id++
		params := map[string]any{"name": "increase-counter", "arguments": map[string]any{"current": 10}, "_meta": meta}
		if state != "" {
			params["requestState"], params["inputResponses"] = state, responses
		}
		res := r.PostJSON("/mcp", map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params},
			"accept", "application/json, text/event-stream", "mcp-protocol-version", ver, "mcp-method", "tools/call", "mcp-name", "increase-counter")
		var rpc struct {
			Result *result         `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := res.JSON(&rpc); err != nil || res.Code != 200 || rpc.Result == nil {
			return nil, fmt.Errorf("tools/call: %v %s", err, res)
		}
		return rpc.Result, nil
	}
	accept := func(content map[string]any) map[string]any {
		return map[string]any{"action": "accept", "content": content}
	}
	r1, err := call("", nil)
	if err != nil {
		return err
	}
	if r1.ResultType != "input_required" || !strings.Contains(r1.InputRequests["amount"].Params.Message, "By how much") {
		return fmt.Errorf("round 1: %+v", r1)
	}
	r2, err := call(r1.RequestState, map[string]any{"amount": accept(map[string]any{"amount": 5})})
	if err != nil {
		return err
	}
	if r2.ResultType != "input_required" || r2.InputRequests["confirmation"].Params.Message != "Increase 10 by 5?" {
		return fmt.Errorf("round 2: %+v", r2)
	}
	r3, err := call(r2.RequestState, map[string]any{"confirmation": accept(map[string]any{"confirm": true})})
	if err != nil {
		return err
	}
	if len(r3.Content) != 1 || r3.Content[0].Text != "Counter increased by 5; next value is 15" {
		return fmt.Errorf("round 3: %+v", r3)
	}
	r.Note("MRTR: input_required(amount) → input_required(confirmation) → %q", r3.Content[0].Text)
	return nil
}

var hiddenStateRe = regexp.MustCompile(`name="state" value="([^"]+)"`)

// probeMCPWorkerAuth: the OAuth 2.1 flow an MCP client runs against the
// example (dynamic registration, PKCE authorization code, token), then the
// tools with the token.
func probeMCPWorkerAuth(r *Run) error {
	resource := r.Base + "/mcp"
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil { // the React client
		return err
	}
	noTok := r.PostJSON("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, "accept", "application/json, text/event-stream")
	if noTok.Code != 401 {
		return fmt.Errorf("/mcp without a token: want 401, got %s", noTok)
	}
	var meta struct {
		RegistrationEndpoint string `json:"registration_endpoint"`
		TokenEndpoint        string `json:"token_endpoint"`
	}
	if err := r.Get("/.well-known/oauth-authorization-server").JSON(&meta); err != nil || !strings.HasSuffix(meta.TokenEndpoint, "/oauth/token") {
		return fmt.Errorf("authorization server metadata: %v %+v", err, meta)
	}
	const redirect = "http://127.0.0.1:9/callback"
	var client struct {
		ClientID string `json:"client_id"`
	}
	reg := r.PostJSON("/oauth/register", map[string]any{"client_name": "tw-compat", "redirect_uris": []string{redirect}, "token_endpoint_auth_method": "none"})
	if err := reg.JSON(&client); err != nil || client.ClientID == "" {
		return fmt.Errorf("register: %v %s", err, reg)
	}
	verifier := base64.RawURLEncoding.EncodeToString([]byte("tw-compat-pkce-verifier-0123456789abcdef"))
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {redirect}, "state": {"compat-state"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {resource}}
	page := r.Get("/authorize?" + q.Encode())
	if err := page.Want(200, "Authorization Request", "tw-compat"); err != nil {
		return err
	}
	st := hiddenStateRe.FindStringSubmatch(page.Body)
	if st == nil {
		return fmt.Errorf("no state field in the approval page")
	}
	app := r.PostForm("/authorize", url.Values{"state": {st[1]}}.Encode())
	loc, _ := url.Parse(app.Header.Get("location"))
	if app.Code != 302 || loc == nil || loc.Query().Get("code") == "" || loc.Query().Get("state") != "compat-state" {
		return fmt.Errorf("approve: %s → %q", app, app.Header.Get("location"))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	tr := r.PostForm("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {client.ClientID}, "code_verifier": {verifier}, "resource": {resource}}.Encode())
	if err := tr.JSON(&tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("token: %v %s", err, tr)
	}
	r.Note("OAuth: registered a client, approved, exchanged the code for a token")
	r.Hdr = []string{"authorization", "Bearer " + tok.AccessToken}
	m, name, err := r.MCPConnect("/mcp")
	if err != nil {
		return err
	}
	if name != "Authenticated MCP Server" {
		return fmt.Errorf("serverInfo.name %q", name)
	}
	if out, err := m.Tool("hello", map[string]any{}); err != nil || out != "Hello, Demo User!" {
		return fmt.Errorf("hello: %q %v", out, err)
	}
	if out, err := m.Tool("whoami", map[string]any{}); err != nil || !strings.Contains(out, `"email": "demo@example.com"`) || !strings.Contains(out, client.ClientID) {
		return fmt.Errorf("whoami: %q %v", out, err)
	}
	return nil
}

// probeAgentsWorkflows: submit a task; the workflow's steps run and report
// progress to the agent, which broadcasts it; restart it; list it. The
// example throws after each step 9 times in 10 on purpose (`maybeThrow`,
// "this is intentional for testing"), so a run is expected to end in that
// error, and the approval step behind two of those throws is not reached.
func probeAgentsWorkflows(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	a, err := r.Agent("/agents/task-agent/compat")
	if err != nil {
		return err
	}
	defer a.Close()
	out, err := a.RPC("submitTask", "compat task")
	if err != nil {
		return err
	}
	var wf struct{ WorkflowID string }
	if json.Unmarshal(out, &wf); wf.WorkflowID == "" {
		return fmt.Errorf("submitTask: %s", out)
	}
	update := func(sub ...string) func(string) bool {
		return func(m string) bool {
			if !strings.Contains(m, "workflow:updated") {
				return false
			}
			for _, s := range sub {
				if strings.Contains(m, s) {
					return true
				}
			}
			return false
		}
	}
	// reportProgress → the agent's onWorkflowProgress → broadcast.
	if _, err := a.Wait(60*time.Second, update(`"step":"validate"`)); err != nil {
		return fmt.Errorf("the first progress report: %v", err)
	}
	// The step ran; then the intentional throw (→ onWorkflowError), or 1 in
	// 10 the next step's report.
	m, err := a.Wait(60*time.Second, update("Random failure in", `"step":"process"`, `"percent":0.25`))
	if err != nil {
		return fmt.Errorf("after the validate step: %v", err)
	}
	if strings.Contains(m, "Random failure in") {
		r.Note("the workflow ended in the example's intentional random failure (onWorkflowError reached the client)")
	} else {
		r.Note("the workflow got past validate")
	}
	a.Forget()
	if _, err := a.RPC("restart", wf.WorkflowID); err != nil {
		return err
	}
	if _, err := a.Wait(60*time.Second, update(`"step":"validate"`)); err != nil {
		return fmt.Errorf("progress after restart: %v", err)
	}
	out, err = a.RPC("listWorkflows")
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), wf.WorkflowID) {
		return fmt.Errorf("listWorkflows: %s", tail(string(out), 300))
	}
	r.Note("not reached: waitForApproval and the example's Approve button (behind two intentional 90%% random failures)")
	return nil
}

// probePlayground: one check per demo family that needs no Cloudflare-only
// binding (the AI, email, voice and codemode demos do).
func probePlayground(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	agent := func(class string, f func(a *AgentWS) error) error {
		a, err := r.Agent("/agents/" + class + "/compat")
		if err != nil {
			return fmt.Errorf("%s: %v", class, err)
		}
		defer a.Close()
		if err := f(a); err != nil {
			return fmt.Errorf("%s: %v", class, err)
		}
		return nil
	}
	want := func(out []byte, err error, sub string) error {
		if err != nil {
			return err
		}
		if !strings.Contains(string(out), sub) {
			return fmt.Errorf("want %s in %s", sub, tail(string(out), 300))
		}
		return nil
	}
	steps := []func() error{
		// Core: state.
		func() error {
			return agent("state-agent", func(a *AgentWS) error {
				out, err := a.RPC("setCounter", 41)
				if err := want(out, err, `"counter":41`); err != nil {
					return err
				}
				if _, err := a.RPC("increment"); err != nil {
					return err
				}
				_, err = a.Wait(15*time.Second, func(m string) bool {
					return strings.Contains(m, "cf_agent_state") && strings.Contains(m, `"counter":42`)
				})
				return err
			})
		},
		// Core: callable, and an error from a callable.
		func() error {
			return agent("callable-agent", func(a *AgentWS) error {
				out, err := a.RPC("add", 40, 2)
				if err := want(out, err, "42"); err != nil {
					return err
				}
				if _, err := a.RPC("throwError", "compat boom"); err == nil || !strings.Contains(err.Error(), "compat boom") {
					return fmt.Errorf("throwError: %v", err)
				}
				return nil
			})
		},
		// Core: streaming RPC.
		func() error {
			return agent("streaming-agent", func(a *AgentWS) error {
				out, chunks, err := a.RPCStream("streamNumbers", 3)
				if err := want(out, err, `"total":3`); err != nil {
					return err
				}
				if len(chunks) != 3 || !strings.Contains(chunks[2], `"progress":"3/3"`) {
					return fmt.Errorf("stream chunks: %v", chunks)
				}
				return nil
			})
		},
		// Core: schedule().
		func() error {
			return agent("schedule-agent", func(a *AgentWS) error {
				if _, err := a.RPC("scheduleTask", 1, "compat tick"); err != nil {
					return err
				}
				_, err := a.Wait(40*time.Second, func(m string) bool {
					return strings.Contains(m, "schedule_executed") && strings.Contains(m, "compat tick")
				})
				return err
			})
		},
		// Core: this.sql.
		func() error {
			return agent("sql-agent", func(a *AgentWS) error {
				if _, err := a.RPC("insertRecord", "compat-key", "compat-value"); err != nil {
					return err
				}
				out, err := a.RPC("getRecords")
				return want(out, err, "compat-value")
			})
		},
		// Core: routing by name, and the example's own /custom-routing path.
		func() error {
			return agent("routing-agent", func(a *AgentWS) error {
				out, err := a.RPC("getInfo")
				return want(out, err, `"agentName":"compat"`)
			})
		},
		// Multi-agent: a supervisor creates children and calls them (DO RPC).
		func() error {
			return agent("supervisor-agent", func(a *AgentWS) error {
				for _, c := range []string{"compat-child-1", "compat-child-2"} {
					if _, err := a.RPC("createChild", c); err != nil {
						return err
					}
				}
				if _, err := a.RPC("incrementAll"); err != nil {
					return err
				}
				out, err := a.RPC("getStats")
				return want(out, err, `"totalChildren":2`)
			})
		},
		// MCP: the playground's McpAgent.
		func() error {
			m, name, err := r.MCPConnect("/mcp-server")
			if err != nil {
				return err
			}
			out, err := m.Tool("roll_dice", map[string]any{"sides": 6, "count": 2})
			if err != nil || !strings.Contains(out, `"rolls"`) {
				return fmt.Errorf("mcp %s roll_dice: %q %v", name, out, err)
			}
			return nil
		},
		// Workflows: AgentWorkflow progress to the agent to the client.
		func() error {
			return agent("basic-workflow-agent", func(a *AgentWS) error {
				if _, err := a.RPC("startWorkflow", "compat", 2); err != nil {
					return err
				}
				if _, err := a.Wait(60*time.Second, func(m string) bool { return strings.Contains(m, "workflow_progress") }); err != nil {
					return err
				}
				_, err := a.Wait(90*time.Second, func(m string) bool { return strings.Contains(m, "workflow_complete") })
				return err
			})
		},
	}
	// Every demo runs, so the result names each one that fails.
	var errs []string
	for _, f := range steps {
		if err := f(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d of %d demos failed: %s", len(errs), len(steps), strings.Join(errs, "; "))
	}
	return nil
}
