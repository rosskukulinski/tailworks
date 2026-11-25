package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestMCPServer: tailworks' MCP server (mcp.go) behind the WhoIs gate. Who
// gets which tools (read for admins and MCPCap holders; deploy, kill,
// undeploy only from an MCPCap grant naming them; the local bridge only
// with Tw-MCP-Tools), that an MCPCap-only caller reaches /mcp and nothing
// else, that tools run the /v1 handlers (their errors and data), and the
// JSON-RPC edges.
func TestMCPServer(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	srv := &Server{M: e.m, Version: "v-test", Self: SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}}
	tn := v1Tenant(t, e, "op.ts.net", nil)
	mcpVals := func(v ...string) map[string][]json.RawMessage {
		var out []json.RawMessage
		for _, s := range v {
			out = append(out, json.RawMessage(s))
		}
		return map[string][]json.RawMessage{MCPCap: out}
	}
	peers := map[string]WhoIsInfo{
		"admin": {NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}},
		"eve":   {NodeID: "nEVE", Login: "eve@example.com"},
		"ro":    {NodeID: "nRO", Name: "laptop.t.ts.net.", Tags: []string{"tag:agent"}, Login: "tagged-devices", Caps: []string{MCPCap}, CapValues: mcpVals(`{}`)},
		"killer": {NodeID: "nKILL", Login: "bob@example.com", Caps: []string{MCPCap},
			CapValues: mcpVals(`{"tools":["kill"]}`, `{"tools":["undeploy"]}`)},
		"all":  {NodeID: "nALL", Login: "carol@example.com", Caps: []string{MCPCap}, CapValues: mcpVals(`{"tools":["*"]}`)},
		"host": {NodeID: "nHOST", Tags: []string{HostTag}, Login: "tagged-devices"},
	}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		if wi, ok := peers[strings.TrimSuffix(addr, ":1")]; ok {
			return wi, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown %s", addr)
	}
	tailnet := srv.TailnetHandler(whois, srv.Handler())
	local := LocalHandler(srv.Handler(), "tok")

	type resp struct {
		status int
		body   map[string]any
	}
	post := func(from, method, path, body string, hdr ...string) resp {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = from + ":1"
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		if from == "local" {
			r.Header.Set(HostTokenHeader, "tok")
			local.ServeHTTP(w, r)
		} else {
			tailnet.ServeHTTP(w, r)
		}
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return resp{w.Code, m}
	}
	rpc := func(from, method string, params any, hdr ...string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
		r := post(from, "POST", MCPPath, string(b), hdr...)
		if r.status != 200 || r.body["id"] != float64(7) {
			t.Fatalf("%s %s: %d %v", from, method, r.status, r.body)
		}
		return r.body
	}
	tools := func(from string, hdr ...string) []string {
		t.Helper()
		var out []string
		for _, x := range rpc(from, "tools/list", map[string]any{}, hdr...)["result"].(map[string]any)["tools"].([]any) {
			out = append(out, x.(map[string]any)["name"].(string))
		}
		return out
	}
	call := func(from, tool string, args map[string]any, hdr ...string) (map[string]any, map[string]any) {
		t.Helper()
		res := rpc(from, "tools/call", map[string]any{"name": tool, "arguments": args}, hdr...)["result"].(map[string]any)
		sc, _ := res["structuredContent"].(map[string]any)
		if res["isError"] == true {
			return nil, sc["error"].(map[string]any)
		}
		return sc, nil
	}
	read := []string{"whoami", "status", "ls", "logs", "audit"}

	// Who gets what.
	for _, tc := range []struct {
		from string
		hdr  []string
		want []string
	}{
		{"admin", nil, read},
		{"ro", nil, read},
		{"killer", nil, append(slices.Clone(read), "kill", "undeploy")},
		{"all", nil, append(slices.Clone(read), "deploy", "kill", "undeploy")},
		{"all", []string{MCPToolsHeader, "ls,kill"}, []string{"ls", "kill"}}, // the header narrows
		{"ro", []string{MCPToolsHeader, "kill"}, nil},                        // … never widens a tailnet caller
		{"local", nil, read}, // the bridge: read only by default
		{"local", []string{MCPToolsHeader, "kill"}, []string{"kill"}}, // --allow kill (narrowed to it)
		{"local", []string{MCPToolsHeader, "*"}, append(slices.Clone(read), "deploy", "kill", "undeploy")},
	} {
		if got := tools(tc.from, tc.hdr...); !slices.Equal(got, tc.want) {
			t.Errorf("%s %v: tools %v, want %v", tc.from, tc.hdr, got, tc.want)
		}
	}

	// No cap at all: refused at the gate with the MCP grant to add.
	r := post("eve", "POST", MCPPath, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if r.status != 403 || r.body["code"] != "not_admin" || !strings.Contains(fmt.Sprint(r.body["snippet"]), MCPCap) {
		t.Fatalf("no cap: %d %v", r.status, r.body)
	}
	// An MCPCap-only caller reaches /mcp and nothing else.
	for _, path := range []string{"/v1/status", "/v1/tenants", "/ui/", "/v2/"} {
		if r := post("ro", "GET", path, ""); r.status != 403 {
			t.Errorf("MCP-only caller GET %s: %d %v", path, r.status, r.body)
		}
	}
	if r := post("host", "POST", MCPPath, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); r.status != 403 {
		t.Fatalf("host: %d %v", r.status, r.body)
	}

	// initialize.
	init := rpc("ro", "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t"}})["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "tw" {
		t.Fatalf("initialize: %v", init)
	}

	// Tools run the /v1 handlers, as the caller (elevated for the op only).
	ls, errv := call("ro", "ls", nil)
	if errv != nil || ls["tenantID"] != tn || ls["tenant"] != "op.ts.net" {
		t.Fatalf("ls: %v %v", ls, errv)
	}
	// ls asks mgmt for live objects only, so tombstones never fill the page;
	// all=true has them.
	for i, st := range []string{StatusKilled, StatusReady} {
		id := fmt.Sprintf("room-mcpls%d", i)
		if err := e.store.CreateObject(context.Background(), Object{ID: id, TenantID: tn, Workload: "w", Namespace: "Room", Name: id, Hostname: id, RootID: id, Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	for _, all := range []bool{false, true} {
		got, errv := call("ro", "ls", map[string]any{"all": all})
		var ids []string
		for _, o := range got["objects"].([]any) {
			ids = append(ids, fmt.Sprint(o.(map[string]any)["objectID"]))
		}
		if want := map[bool]string{false: "room-mcpls1", true: "room-mcpls0,room-mcpls1"}[all]; errv != nil || strings.Join(ids, ",") != want {
			t.Errorf("ls all=%v: objects %v, want %s (%v)", all, ids, want, errv)
		}
	}
	if _, errv := call("ro", "ls", map[string]any{"tenant": "nope.ts.net"}); errv == nil || errv["code"] != "unknown_tenant" {
		t.Fatalf("ls unknown tenant: %v", errv)
	}
	if st, errv := call("admin", "status", nil); errv != nil || st["ok"] != true {
		t.Fatalf("status: %v %v", st, errv)
	}
	if au, errv := call("ro", "audit", map[string]any{"limit": 5}); errv != nil || au["items"] == nil || au["verified"] != true {
		t.Fatalf("audit: %v %v", au, errv)
	}
	who, _ := call("ro", "whoami", nil)
	if who["kind"] != "mcp" || !strings.Contains(fmt.Sprint(who["howToGrant"]), `"tools":["deploy","kill","undeploy"]`) {
		t.Fatalf("whoami: %v", who)
	}
	// Not granted: cap_missing with the grant naming the tool.
	if _, errv := call("ro", "kill", map[string]any{"object": "o1", "reason": "x"}); errv == nil || errv["code"] != "cap_missing" ||
		!strings.Contains(fmt.Sprint(errv["snippet"]), `"src": ["tag:agent"]`) || !strings.Contains(fmt.Sprint(errv["snippet"]), `"tools":["kill"]`) {
		t.Fatalf("kill without grant: %v", errv)
	}
	if _, errv := call("admin", "undeploy", map[string]any{"workload": "w", "reason": "x"}); errv == nil || errv["code"] != "cap_missing" {
		t.Fatalf("an admin without the MCP grant must not undeploy over MCP: %v", errv)
	}
	if _, errv := call("local", "kill", map[string]any{"object": "o1", "reason": "x"}); errv == nil || errv["code"] != "cap_missing" || errv["snippet"] != "tw mcp --allow kill" {
		t.Fatalf("local kill without --allow: %v", errv)
	}
	// Granted: the /v1 handler's own answer.
	if _, errv := call("killer", "kill", map[string]any{"object": "o-missing", "reason": "x"}); errv == nil || errv["code"] != "not_found" {
		t.Fatalf("granted kill of a missing object: %v", errv)
	}
	if _, errv := call("killer", "undeploy", map[string]any{"workload": "nope", "reason": "x"}); errv == nil || errv["code"] != "unknown_workload" {
		t.Fatalf("granted undeploy of a missing workload: %v", errv)
	}
	if _, errv := call("killer", "kill", map[string]any{"object": "o1"}); errv == nil || errv["code"] != "bad_request" {
		t.Fatalf("kill without a reason: %v", errv)
	}
	if _, errv := call("all", "deploy", map[string]any{"workload": "w", "bundle": "!!"}); errv == nil || errv["code"] != "bad_request" {
		t.Fatalf("deploy with a bad bundle: %v", errv)
	}

	// JSON-RPC edges.
	if r := post("ro", "POST", MCPPath, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); r.status != 202 {
		t.Fatalf("notification: %d", r.status)
	}
	if r := post("ro", "GET", MCPPath, ""); r.status != 405 {
		t.Fatalf("GET /mcp: %d", r.status)
	}
	if r := post("ro", "POST", MCPPath, `{`); r.body["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("parse error: %v", r.body)
	}
	if b := rpc("ro", "tools/call", map[string]any{"name": "nope"}); b["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("unknown tool: %v", b)
	}
	if b := rpc("ro", "nope/method", nil); b["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method: %v", b)
	}
	// Browsers are refused (CSRF / DNS rebinding), even same-origin.
	if r := post("all", "POST", MCPPath, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Origin", "http://example.com", "Sec-Fetch-Mode", "cors"); r.status != 403 {
		t.Fatalf("browser POST /mcp: %d", r.status)
	}
}
