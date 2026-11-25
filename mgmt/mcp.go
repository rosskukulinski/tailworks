package mgmt

// tailworks' own MCP server (issue #25, ADR-035): POST /mcp on the same
// listeners and behind the same WhoIs gate as /v1, so a coding agent on
// any tailnet machine can operate tailworks with no credentials on disk.
//
// Transport: MCP Streamable HTTP, stateless: every POST is one JSON-RPC
// request (or notification), answered with application/json. No sessions,
// no server-to-client stream (GET is 405).
//
// Tools call the /v1 handlers in process (mcpCall), so validation, roles,
// audit and error codes are exactly the API's. Who may call which tool:
//
//   - read tools (whoami, status, ls, logs, audit): a tailworks admin (AdminCap)
//     or any holder of MCPCap;
//   - deploy, kill, undeploy: only when an MCPCap grant names them
//     ({"tools": ["kill"]}, or "*"). Being an admin is not enough: an agent
//     gets destructive tools only from a grant that says so;
//   - the local operator (mgmt's own node, the unix socket: the `tw mcp`
//     bridge on the tailworks host) gets the read tools, or exactly the tools the
//     bridge lists in Tw-MCP-Tools (`tw mcp --allow kill` sends the read
//     tools and kill). The operator already holds every power; this keeps
//     an agent on that machine read-only unless the operator opted in;
//   - for a tailnet caller, Tw-MCP-Tools only narrows the granted set;
//   - hosts get nothing.
//
// A caller holding only MCPCap reaches /mcp and nothing else
// (TailnetHandler): it is never a /v1, /ui or /v2 principal.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// MCPCap is the app capability that admits a tailnet caller to /mcp and
// grants it tools: [{"tools": ["kill", "undeploy"]}] ("*" = every tool).
// With no tools listed it grants the read tools.
const MCPCap = "github.com/rosskukulinski/tailworks/cap/mcp"

// MCPPath is where the MCP server is mounted.
const MCPPath = "/mcp"

// MCPToolsHeader narrows the tools of one request (the `tw mcp` bridge's
// --allow); for the local operator it is also what enables the
// destructive tools.
const MCPToolsHeader = "Tw-MCP-Tools"

// mcpProtocolVersions are the MCP revisions this server speaks, newest
// first; initialize echoes the client's if listed.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// MCPGrantSnippet is the policy grant that gives src these MCP tools.
func MCPGrantSnippet(src string, tools ...string) string {
	v := "{}"
	if len(tools) > 0 {
		b, _ := json.Marshal(map[string][]string{"tools": tools})
		v = string(b)
	}
	return `{"src": ["` + src + `"], "dst": ["` + MgmtTag + `"], "ip": ["tcp:443", "tcp:80"], "app": {"` + MCPCap + `": [` + v + `]}}`
}

// mcpTool is one tool: its MCP definition and how it runs.
type mcpTool struct {
	Name        string
	Description string
	Input       map[string]any // JSON Schema of the arguments
	// Kind is "read", "write" (deploy) or "destructive" (kill, undeploy).
	Kind string
	run  func(c *mcpCtx, args map[string]any) (any, error)
}

// mcpCtx is one tools/call: the caller and how to reach /v1.
type mcpCtx struct {
	ctx context.Context
	s   *Server
	api http.Handler
	p   Principal
	hdr http.Header // the caller's request headers (Tw-Actor on the bridge)

	// scoped: a tenant admin's tool, run as the caller rather than as an
	// admin (tenantscope_mcp.go).
	scoped bool
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func intProp(desc string, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "maximum": max, "description": desc}
}
func obj(required []string, props map[string]any) map[string]any {
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

var tenantArg = strProp("tenant: its tailnet (example.ts.net) or tenant ID; optional when tailworks serves one tenant")

// mcpTools is every tool, in tools/list order (set in init: whoami
// lists the tools itself).
var mcpTools []mcpTool

func init() { mcpTools = mcpToolList() }

func mcpToolList() []mcpTool {
	return []mcpTool{
		{Name: "whoami", Kind: "read", Description: "Who tw thinks you are on the tailnet, and which tw tools you may call (and the grant that would add the others).",
			Input: obj(nil, map[string]any{}), run: mcpWhoami},
		{Name: "status", Kind: "read", Description: "Health of tailworks: mgmt, hosts, tenants, deployments (GET /v1/status).",
			Input: obj(nil, map[string]any{}), run: mcpStatus},
		{Name: "ls", Kind: "read", Description: "Deployments and objects (each Durable Object instance with its own tailnet node) of a tenant, like `tw ls`.",
			Input: obj(nil, map[string]any{"tenant": tenantArg, "workload": strProp("only this workload"), "all": boolProp("include killed objects and stopped deployments")}), run: mcpLs},
		{Name: "logs", Kind: "read", Description: "A workload's request records (one per request: method, path, status, outcome, caller, its console output and exceptions), or with console=true its raw output, like `tw logs`.",
			Input: obj([]string{"workload"}, map[string]any{"tenant": tenantArg, "workload": strProp("the workload"), "n": intProp("how many records (or lines with console) from the end; default 50", 1000),
				"console": boolProp("the app's raw output instead of request records"), "errors": boolProp("only failed requests (status >= 400, exceptions)"),
				"status": strProp("only this status (404) or class (5xx)"), "object": strProp("only this object (ID, Class/name, Class or name)"),
				"outcome": strProp("only this outcome (ok, exception, canceled, …)"), "caller": strProp("only callers whose login or node name contains this (public: Funnel requests)"),
				"trace": strProp("only this trace ID")}), run: mcpLogs},
		{Name: "audit", Kind: "read", Description: "The audit trail, like `tw audit`: who created, called, spawned or killed what, when and why.",
			Input: obj(nil, map[string]any{"tenant": tenantArg, "object": strProp("records about this object ID"), "human": strProp("records acting for this principal (login or caller node)"), "trace": strProp("records of this trace ID"), "limit": intProp("at most this many records; default 100", 500)}), run: mcpAudit},
		{Name: "deploy", Kind: "write", Description: "Deploy a workload. With bundle (base64 of the tar `tw deploy` uploads) it deploys that; without, it redeploys the workload's current bundle (with replace=true, re-placed on the best host). Returns at once; poll ls for status.",
			Input: obj([]string{"workload"}, map[string]any{"tenant": tenantArg, "workload": strProp("the workload"), "bundle": strProp("base64 bundle tar (optional)"),
				"replace": boolProp("re-place the workload on a better host"), "dryRun": boolProp("only preflight the bundle against the tailnet policy")}), run: mcpDeploy},
		{Name: "kill", Kind: "destructive", Description: "Kill an object: cut it off the tailnet in under a second and delete its node. No undo. Only after the user agreed to kill this object.",
			Input: obj([]string{"object", "reason"}, map[string]any{"object": strProp("the object ID (from ls)"), "cascade": boolProp("also kill everything it spawned; default true, like tw kill (with false the reaper kills them within seconds)"), "reason": strProp("why (recorded in the audit trail)")}), run: mcpKill},
		{Name: "undeploy", Kind: "destructive", Description: "Stop a workload: kill all its objects and release its host. No undo. Only after the user agreed to undeploy this workload.",
			Input: obj([]string{"workload", "reason"}, map[string]any{"tenant": tenantArg, "workload": strProp("the workload"), "reason": strProp("why (recorded in the audit trail)")}), run: mcpUndeploy},
	}
}

func mcpToolByName(name string) (mcpTool, bool) {
	for _, t := range mcpTools {
		if t.Name == name {
			return t, true
		}
	}
	return mcpTool{}, false
}

// mcpCapTools is the union of the tools in the caller's MCPCap values;
// ok is whether it holds MCPCap at all.
func mcpCapTools(p Principal) (tools map[string]bool, ok bool) {
	tools = map[string]bool{}
	vals, ok := p.CapValues[MCPCap]
	if !ok && slices.Contains(p.Caps, MCPCap) {
		ok = true
	}
	for _, raw := range vals {
		var v struct {
			Tools []string `json:"tools"`
		}
		if json.Unmarshal(raw, &v) == nil {
			for _, t := range v.Tools {
				tools[t] = true
			}
		}
	}
	return tools, ok
}

// mcpAllowed is the set of tools p may call in this request.
func (s *Server) mcpAllowed(p Principal, hdr http.Header) map[string]bool {
	out := map[string]bool{}
	var narrow map[string]bool
	if h := strings.TrimSpace(hdr.Get(MCPToolsHeader)); h != "" {
		narrow = map[string]bool{}
		for _, t := range strings.Split(h, ",") {
			narrow[strings.TrimSpace(t)] = true
		}
	}
	add := func(name string) {
		if narrow == nil || narrow[name] || narrow["*"] {
			out[name] = true
		}
	}
	switch p.Kind {
	case "local":
		// The operator's own bridge: what it asks for (`tw mcp` sends the
		// read tools plus --allow), else the read tools.
		for _, t := range mcpTools {
			if (narrow == nil && t.Kind == "read") || (narrow != nil && (narrow[t.Name] || narrow["*"])) {
				out[t.Name] = true
			}
		}
		return out
	case "user", "mcp":
		for name := range s.mcpPlainTools(p) {
			add(name)
		}
		for name := range tenantMCPTools(p) { // tenantscope_mcp.go
			add(name)
		}
	}
	return out
}

// mcpPlainTools are the tools a tailnet caller has as an admin or from
// MCPCap (they run as an admin, ADR-035).
func (s *Server) mcpPlainTools(p Principal) map[string]bool {
	out := map[string]bool{}
	granted, hasCap := mcpCapTools(p)
	admin := p.Kind == "user" && s.allowed(p, roleAdmin)
	for _, t := range mcpTools {
		if (t.Kind == "read" && (admin || hasCap)) || granted[t.Name] || granted["*"] {
			out[t.Name] = true
		}
	}
	return out
}

// ---- JSON-RPC ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rerr *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// mcpHandler serves POST /mcp; api is the /v1 role gate the tools call.
func (s *Server) mcpHandler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		switch {
		case p.Kind == "":
			writeErr(w, r, errf(401, "no_identity", "unauthenticated"))
			return
		case p.Kind == "host":
			writeErr(w, r, errf(403, "forbidden", "hosts may not use the MCP server"))
			return
		case r.Method != http.MethodPost:
			w.Header().Set("Allow", "POST")
			writeErr(w, r, errf(405, "bad_request", "POST JSON-RPC to %s (stateless Streamable HTTP; no server stream)", MCPPath))
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeRPC(w, nil, nil, &rpcError{-32700, "can't read the body"})
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeRPC(w, nil, nil, &rpcError{-32700, "parse error: " + err.Error()})
			return
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			writeRPC(w, req.ID, nil, &rpcError{-32600, "invalid request: want a JSON-RPC 2.0 request (batches are not supported)"})
			return
		}
		if len(req.ID) == 0 { // a notification (notifications/initialized, …)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		allowed := s.mcpAllowed(p, r.Header)
		switch req.Method {
		case "initialize":
			var ip struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &ip)
			v := mcpProtocolVersions[0]
			if slices.Contains(mcpProtocolVersions, ip.ProtocolVersion) {
				v = ip.ProtocolVersion
			}
			writeRPC(w, req.ID, map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "tw", "version": s.Version},
				"instructions": "tw runs Workers and Durable Objects on a tailnet; each object is its own tailnet node. " +
					"Use ls to find workloads and object IDs, logs to read request records, audit to see who did what. " +
					"kill and undeploy have no undo: call them only after the user agreed to that specific action. " +
					"Errors carry a tailworks code (docs/errors.md) and a hint.",
			}, nil)
		case "ping":
			writeRPC(w, req.ID, map[string]any{}, nil)
		case "tools/list":
			var tools []map[string]any
			for _, t := range mcpTools {
				if !allowed[t.Name] {
					continue
				}
				tools = append(tools, map[string]any{
					"name": t.Name, "description": t.Description, "inputSchema": t.Input,
					"annotations": map[string]any{"readOnlyHint": t.Kind == "read", "destructiveHint": t.Kind == "destructive", "idempotentHint": t.Kind == "read", "openWorldHint": false},
				})
			}
			if tools == nil {
				tools = []map[string]any{}
			}
			writeRPC(w, req.ID, map[string]any{"tools": tools}, nil)
		case "tools/call":
			var cp struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &cp); err != nil {
				writeRPC(w, req.ID, nil, &rpcError{-32602, "invalid params: " + err.Error()})
				return
			}
			t, ok := mcpToolByName(cp.Name)
			if !ok {
				writeRPC(w, req.ID, nil, &rpcError{-32602, "unknown tool " + strconv.Quote(cp.Name)})
				return
			}
			if !allowed[t.Name] {
				writeRPC(w, req.ID, toolError(s.mcpNotGranted(p, t)), nil)
				return
			}
			if cp.Arguments == nil {
				cp.Arguments = map[string]any{}
			}
			c := &mcpCtx{ctx: r.Context(), s: s, api: api, p: p, hdr: r.Header}
			if (p.Kind == "user" || p.Kind == "mcp") && !s.mcpPlainTools(p)[t.Name] {
				// Only a tenant-admin value allows it: run as the caller,
				// limited to its MCP values (tenantscope_mcp.go).
				c.p, c.scoped = tenantMCPPrincipal(p), true
			}
			out, err := t.run(c, cp.Arguments)
			if err != nil {
				writeRPC(w, req.ID, toolError(err), nil)
				return
			}
			writeRPC(w, req.ID, toolResult(out), nil)
		default:
			writeRPC(w, req.ID, nil, &rpcError{-32601, "method not found: " + req.Method})
		}
	})
}

// mcpNotGranted is the cap_missing error for a tool the caller lacks,
// with the grant that would add it.
func (s *Server) mcpNotGranted(p Principal, t mcpTool) *Error {
	if p.Kind == "local" {
		e := errf(403, "cap_missing", "%s is not enabled on this bridge: the operator enables %s tools explicitly", t.Name, t.Kind)
		e.Snippet = "tw mcp --allow " + t.Name
		return e
	}
	if s.tenantScoped(p) && !s.mcpPlainTools(p)[t.Name] {
		return tenantMCPNotGranted(p, t) // tenantscope_mcp.go
	}
	src := p.Login
	if len(p.Tags) > 0 {
		src = p.Tags[0]
	}
	e := errf(403, "cap_missing", "%s may not call the %s tool: it needs a %s grant to %s naming it", p.Login, t.Name, MCPCap, MgmtTag)
	e.Snippet = MCPGrantSnippet(src, t.Name)
	e.Missing = map[string]any{"src": src, "dst": MgmtTag, "cap": MCPCap, "tool": t.Name}
	return e
}

// toolResult is a successful tools/call result: the JSON as text, and as
// structuredContent when it is an object.
func toolResult(v any) map[string]any {
	b, _ := json.MarshalIndent(v, "", "  ")
	res := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "isError": false}
	if _, ok := v.(map[string]any); ok {
		res["structuredContent"] = v
	}
	return res
}

// toolError is a failed tools/call: the tailworks error ({code, message, hint,
// doc_url, …}) the agent acts on.
func toolError(err error) map[string]any {
	e, ok := err.(*Error)
	if !ok {
		e = errf(500, "internal", "%v", err)
	}
	body := map[string]any{"code": e.Code, "message": e.Message, "doc_url": DocBase + e.Code}
	hint := "see the doc_url"
	if e.Snippet != "" {
		body["snippet"] = e.Snippet
		hint = "the grant to add (or the bridge flag): " + e.Snippet
	}
	body["hint"] = hint
	if e.Missing != nil {
		body["missing"] = e.Missing
	}
	if e.TraceID != "" {
		body["trace_id"] = e.TraceID
	}
	b, _ := json.MarshalIndent(map[string]any{"error": body}, "", "  ")
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": map[string]any{"error": body}, "isError": true}
}

// ---- calling /v1 in process ----

// call runs one /v1 request as the caller. A tailnet caller is admitted
// to the operation as an admin: the tool check above is the gate, and the
// audit records name the caller (Actor). out may be nil, *[]byte or a
// pointer to decode into.
func (c *mcpCtx) call(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	p := c.p
	switch {
	case c.scoped:
		p.Kind = "user"
	case p.Kind == "mcp" || p.Kind == "user":
		p.Kind = "user"
		p.Caps = append(slices.Clone(p.Caps), AdminCap)
	case p.Kind == "local":
		actor := c.hdr.Get("Tw-Actor")
		if actor == "" {
			actor = "operator"
		}
		req.Header.Set("Tw-Actor", actor+" (mcp)")
	}
	req = req.WithContext(WithPrincipal(c.ctx, p))
	rec := httptest.NewRecorder()
	c.api.ServeHTTP(rec, req)
	b := rec.Body.Bytes()
	if rec.Code/100 != 2 {
		e := &Error{Status: rec.Code}
		if json.Unmarshal(b, e) != nil || e.Code == "" {
			e = errf(rec.Code, "internal", "%s %s: %d %s", method, path, rec.Code, strings.TrimSpace(string(b)))
		}
		return e
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = b
		return nil
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func argStr(a map[string]any, k string) string { s, _ := a[k].(string); return strings.TrimSpace(s) }
func argBool(a map[string]any, k string) bool  { b, _ := a[k].(bool); return b }
func argInt(a map[string]any, k string, def, max int) (int, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return def, nil
	}
	f, ok := v.(float64)
	if !ok || f < 0 || f != float64(int(f)) || int(f) > max {
		return 0, errf(400, "bad_request", "%s must be an integer 0..%d", k, max)
	}
	return int(f), nil
}

func needStr(a map[string]any, k string) (string, error) {
	if v := argStr(a, k); v != "" {
		return v, nil
	}
	return "", errf(400, "bad_request", "%s is required", k)
}

type listPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// tenant resolves the tenant argument (tailnet or ID); empty means the
// only tenant.
func (c *mcpCtx) tenant(a map[string]any) (Tenant, error) {
	var pg listPage[Tenant]
	if err := c.call("GET", "/v1/tenants?limit=500", nil, &pg); err != nil {
		return Tenant{}, err
	}
	want := argStr(a, "tenant")
	var names []string
	for _, t := range pg.Items {
		names = append(names, t.Tailnet)
		if want != "" && (t.ID == want || t.Tailnet == want) {
			return t, nil
		}
	}
	if want == "" && len(pg.Items) == 1 {
		return pg.Items[0], nil
	}
	if want == "" {
		return Tenant{}, errf(400, "bad_request", "tw serves %d tenants: pass tenant (one of %s)", len(pg.Items), strings.Join(names, ", "))
	}
	return Tenant{}, errf(404, "unknown_tenant", "no tenant %q (tenants: %s)", want, strings.Join(names, ", "))
}

// latestDeployment is the workload's newest deployment.
func (c *mcpCtx) latestDeployment(t Tenant, workload string) (map[string]any, error) {
	var pg listPage[map[string]any]
	if err := c.call("GET", "/v1/deployments?latest=true&limit=500&tenant="+url.QueryEscape(t.ID)+"&workload="+url.QueryEscape(workload), nil, &pg); err != nil {
		return nil, err
	}
	if len(pg.Items) == 0 {
		return nil, errf(404, "unknown_workload", "no deployment of %s for tenant %s", workload, t.Tailnet)
	}
	return pg.Items[len(pg.Items)-1], nil
}

// ---- tools ----

func mcpWhoami(c *mcpCtx, _ map[string]any) (any, error) {
	allowed := c.s.mcpAllowed(c.p, c.hdr)
	var have, missing []string
	for _, t := range mcpTools {
		if allowed[t.Name] {
			have = append(have, t.Name)
		} else {
			missing = append(missing, t.Name)
		}
	}
	out := map[string]any{"kind": c.p.Kind, "login": c.p.Login, "nodeID": c.p.NodeID, "tags": c.p.Tags, "tools": have, "notGranted": missing}
	if len(missing) > 0 {
		if c.p.Kind == "local" {
			out["howToGrant"] = "tw mcp --allow " + strings.Join(missing, ",")
		} else {
			src := c.p.Login
			if len(c.p.Tags) > 0 {
				src = c.p.Tags[0]
			}
			out["howToGrant"] = MCPGrantSnippet(src, missing...)
		}
	}
	return out, nil
}

func mcpStatus(c *mcpCtx, _ map[string]any) (any, error) {
	var v map[string]any
	return v, c.call("GET", "/v1/status", nil, &v)
}

func mcpLs(c *mcpCtx, a map[string]any) (any, error) {
	t, err := c.tenant(a)
	if err != nil {
		return nil, err
	}
	q := "tenant=" + url.QueryEscape(t.ID)
	if w := argStr(a, "workload"); w != "" {
		q += "&workload=" + url.QueryEscape(w)
	}
	var deps, objs listPage[map[string]any]
	if err := c.call("GET", "/v1/deployments?latest=true&limit=500&"+q, nil, &deps); err != nil {
		return nil, err
	}
	all := argBool(a, "all")
	oq := q
	if !all { // killed objects never eat the page (tw ls does the same)
		oq += "&" + url.Values{"state": LiveStatuses}.Encode()
	}
	if err := c.call("GET", "/v1/objects?limit=500&"+oq, nil, &objs); err != nil {
		return nil, err
	}
	shown := []map[string]any{}
	for _, o := range objs.Items {
		if all || !slices.Contains([]string{StatusKilled, StatusFailed, StatusTransferred}, fmt.Sprint(o["status"])) {
			shown = append(shown, o)
		}
	}
	sort.SliceStable(shown, func(i, j int) bool { return fmt.Sprint(shown[i]["createdAt"]) < fmt.Sprint(shown[j]["createdAt"]) })
	dl := []map[string]any{}
	for _, d := range deps.Items {
		if all || d["status"] != "stopped" {
			dl = append(dl, d)
		}
	}
	return map[string]any{"tenant": t.Tailnet, "tenantID": t.ID, "deployments": dl, "objects": shown, "truncated": objs.NextCursor != nil && *objs.NextCursor != ""}, nil
}

func mcpLogs(c *mcpCtx, a map[string]any) (any, error) {
	t, err := c.tenant(a)
	if err != nil {
		return nil, err
	}
	w, err := needStr(a, "workload")
	if err != nil {
		return nil, err
	}
	n, err := argInt(a, "n", 50, 1000)
	if err != nil {
		return nil, err
	}
	q := url.Values{"n": {strconv.Itoa(n)}}
	console := argBool(a, "console")
	if console {
		q.Set("source", "console")
	} else {
		q.Set("source", "requests")
		for _, k := range []string{"status", "object", "outcome", "caller", "trace"} { // every obs.Filter key (TestMCPLogsFilterParity)
			if v := argStr(a, k); v != "" {
				q.Set(k, v)
			}
		}
		if argBool(a, "errors") {
			q.Set("errors", "true")
		}
	}
	var raw []byte
	if err := c.call("GET", "/v1/tenants/"+url.PathEscape(t.ID)+"/workloads/"+url.PathEscape(w)+"/logs?"+q.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	out := map[string]any{"workload": w, "tenant": t.Tailnet}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if console {
		if lines == nil {
			lines = []string{}
		}
		out["source"], out["lines"] = "console", lines
		return out, nil
	}
	recs := []any{}
	var notes []string
	for _, l := range lines {
		host := ""
		if strings.HasPrefix(l, "[") {
			if i := strings.Index(l, "] "); i > 0 {
				host, l = l[1:i], l[i+2:]
			}
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			if host != "" {
				m["host"] = host
			}
			recs = append(recs, m)
		} else if strings.TrimSpace(l) != "" {
			notes = append(notes, l)
		}
	}
	out["source"], out["records"] = "requests", recs
	if notes != nil {
		out["notes"] = notes
	}
	return out, nil
}

func mcpAudit(c *mcpCtx, a map[string]any) (any, error) {
	t, err := c.tenant(a)
	if err != nil {
		return nil, err
	}
	limit, err := argInt(a, "limit", 100, 500)
	if err != nil {
		return nil, err
	}
	q := url.Values{"tenant": {t.ID}, "limit": {strconv.Itoa(max(limit, 1))}}
	if v := argStr(a, "object"); v != "" {
		q.Set("object", v)
	}
	if v := argStr(a, "human"); v != "" {
		q.Set("human", v)
	}
	if v := argStr(a, "trace"); v != "" {
		q.Set("trace", v)
	}
	var v map[string]any
	return v, c.call("GET", "/v1/audit?"+q.Encode(), nil, &v)
}

func mcpDeploy(c *mcpCtx, a map[string]any) (any, error) {
	t, err := c.tenant(a)
	if err != nil {
		return nil, err
	}
	w, err := needStr(a, "workload")
	if err != nil {
		return nil, err
	}
	var bundle []byte
	if b := argStr(a, "bundle"); b != "" {
		if bundle, err = base64.StdEncoding.DecodeString(b); err != nil {
			return nil, errf(400, "bad_request", "bundle is not base64: %v", err)
		}
	} else {
		d, err := c.latestDeployment(t, w)
		if err != nil {
			return nil, err
		}
		if err := c.call("GET", "/v1/deployments/"+url.PathEscape(fmt.Sprint(d["deploymentID"]))+"/bundle", nil, &bundle); err != nil {
			return nil, err
		}
	}
	body := map[string]any{"tenantID": t.ID, "workload": w, "bundle": bundle}
	if argBool(a, "replace") {
		body["replace"] = true
	}
	path := "/v1/deployments"
	if argBool(a, "dryRun") {
		path += "?dryRun=true"
	}
	var v map[string]any
	return v, c.call("POST", path, body, &v)
}

func mcpKill(c *mcpCtx, a map[string]any) (any, error) {
	id, err := needStr(a, "object")
	if err != nil {
		return nil, err
	}
	reason, err := needStr(a, "reason")
	if err != nil {
		return nil, err
	}
	q := url.Values{"reason": {reason}}
	if casc, ok := a["cascade"].(bool); !ok || casc { // cascade unless told not to, like tw kill
		q.Set("cascade", "true")
	}
	var v map[string]any
	if err := c.call("DELETE", "/v1/objects/"+url.PathEscape(id)+"?"+q.Encode(), nil, &v); err != nil {
		return nil, err
	}
	return map[string]any{"object": id, "result": v}, nil
}

func mcpUndeploy(c *mcpCtx, a map[string]any) (any, error) {
	t, err := c.tenant(a)
	if err != nil {
		return nil, err
	}
	w, err := needStr(a, "workload")
	if err != nil {
		return nil, err
	}
	reason, err := needStr(a, "reason")
	if err != nil {
		return nil, err
	}
	d, err := c.latestDeployment(t, w)
	if err != nil {
		return nil, err
	}
	if d["status"] == "stopped" {
		return nil, errf(409, "already_stopped", "%s is already undeployed (%v)", w, d["deploymentID"])
	}
	var v map[string]any
	if err := c.call("DELETE", "/v1/deployments/"+url.PathEscape(fmt.Sprint(d["deploymentID"]))+"?reason="+url.QueryEscape(reason), nil, &v); err != nil {
		return nil, err
	}
	return map[string]any{"workload": w, "deploymentID": d["deploymentID"], "result": v}, nil
}
