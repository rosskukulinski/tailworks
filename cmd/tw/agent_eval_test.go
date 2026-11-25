package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/agenteval"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestAgentEval is issue #25's eval: a scripted coding agent (no LLM)
// replays the commands tailworks' skills tell it to run, in order (scaffold →
// dev → deploy → inspect → logs → teardown), against a real tailworks (tw up
// on testcontrol + a fake Tailscale API, single process). The first deploy
// fails the way a real first deploy does (acl_denied: the app's new tag
// has no tagOwners); the "user" adds the line from the error's hint and
// the agent re-runs the deploy step, as tw-deploy says. Every command in
// between also runs once with --json, and each output is checked against
// schema/cli.schema.json; every error against docs/errors.md.
//
// TW_EVAL_DUMP=<dir> writes every run (args, stdout, stderr) there.
func TestAgentEval(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	// Every peer holds a read-only MCP grant toward mgmt (testcontrol's caps
	// are global): the tailnet MCP client below has it and nothing else.
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}, mgmt.MCPCap: []tailcfg.RawMessage{`{}`}})
	fts := newFakeTS(t)
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-ev-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "api.env")
	writeFakeOAuthFile(t, keyFile)
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: keyFile,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-v-", SingleProcess: true}
	apps := filepath.Join(work, "projects")
	os.MkdirAll(apps, 0o755)
	ag := &agenteval.Agent{T: t, Bin: filepath.Join(bin, "tw"), StateDir: state, Dir: apps}
	t.Cleanup(func() {
		if dir := os.Getenv("TW_EVAL_DUMP"); dir != "" {
			os.MkdirAll(dir, 0o755)
			b, _ := json.MarshalIndent(ag.Log, "", "  ")
			os.WriteFile(filepath.Join(dir, "runs.json"), b, 0o644)
		}
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-4000):]), 4000))
			}
		}
		ag.Exec("down", "--purge")
		os.RemoveAll(state)
	})
	steps, err := agenteval.Steps()
	if err != nil {
		t.Fatal(err)
	}
	ok := func(def string, r agenteval.Run) {
		t.Helper()
		if err := agenteval.CheckResult(def, r); err != nil {
			t.Fatalf("%s %s: %v", r.Ref, strings.Join(r.Args, " "), err)
		}
	}
	doc := func(r agenteval.Run) map[string]any {
		t.Helper()
		v, err := agenteval.Doc(r)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := v.(map[string]any)
		return m
	}
	// okOrError: commands whose success needs what this harness doesn't
	// have; either outcome must follow the contract.
	okOrError := func(def string, r agenteval.Run) {
		t.Helper()
		if r.OK() {
			ok(def, r)
			return
		}
		if _, err := agenteval.CheckError(r); err != nil {
			t.Fatal(err)
		}
	}

	// The operator starts tailworks (the skills tell the agent to ask for this).
	up := ag.Exec(append(e.UpCommand(), "--json")...)
	ok("up", up)

	// scaffold: tw init → cd.
	for _, r := range ag.Replay(steps["scaffold"]) {
		ok("init", r)
	}
	if filepath.Base(ag.Dir) != "hello-mcp" {
		t.Fatalf("after the scaffold step the agent is in %s", ag.Dir)
	}
	// dev: the --once smoke test answers the MCP initialize.
	for _, r := range ag.Replay(steps["dev"]) {
		ok("dev", r)
		if s, _ := doc(r)["smoke"].(map[string]any); s == nil || s["status"] != float64(200) {
			t.Fatalf("tw dev --once smoke: %s", r.Stdout)
		}
	}
	// deploy, first try: the policy has no tagOwners for the app's new tag.
	first := ag.Replay(steps["deploy"])
	de, err := agenteval.CheckError(first[0])
	if err != nil {
		t.Fatal(err)
	}
	hint, _ := de["hint"].(string)
	if de["code"] != "acl_denied" || !strings.Contains(hint, `"tag:tw-wl-hello-mcp"`) || !strings.Contains(hint, "tagOwners") {
		t.Fatalf("first deploy: want acl_denied with the tagOwners line in hint, got %v", de)
	}
	if _, err := agenteval.CheckStream("deploy.event", first[0].Stderr[:strings.LastIndex(strings.TrimRight(first[0].Stderr, "\n"), "\n")+1]); err != nil {
		t.Fatalf("deploy events: %v", err)
	}
	// The user adds what the hint says; the agent runs the step again.
	fts.mu.Lock()
	fts.policy, err = policy.MergeWith(fts.policy, policy.Snippet{TagOwners: map[string][]string{"tag:tw-wl-hello-mcp": {"autogroup:admin", "tag:tw"}}}, policy.Options{})
	fts.etag++
	fts.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// tw-deploy: mgmt re-reads the policy every few seconds, so retry
	// acl_denied for a little while before going back to the user.
	var dep agenteval.Run
	for i := 0; ; i++ {
		dep = ag.Replay(steps["deploy"])[0]
		if dep.OK() || i == 10 || !strings.Contains(dep.Stderr, `"code":"acl_denied"`) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	ok("deploy", dep)
	evs, err := agenteval.CheckStream("deploy.event", dep.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, ev := range evs {
		kinds[ev["event"].(string)] = true
	}
	for _, k := range []string{"start", "preflight", "build", "placed", "status", "ready"} {
		if !kinds[k] {
			t.Fatalf("deploy --json events: no %q in\n%s", k, dep.Stderr)
		}
	}
	d := doc(dep)
	if d["status"] != "active" || d["workload"] != "hello-mcp" {
		t.Fatalf("deploy result: %s", dep.Stdout)
	}
	// inspect: ls and status.
	for _, r := range ag.Replay(steps["inspect"]) {
		ok(r.Args[0], r)
	}
	// logs: NDJSON lines (a fresh MCP server may have logged nothing yet).
	for _, r := range ag.Replay(steps["logs"]) {
		if !r.OK() {
			t.Fatalf("%s: %s", r.Ref, r.Stderr)
		}
		if _, err := agenteval.CheckLogs(r.Stdout); err != nil {
			t.Fatal(err)
		}
	}

	testMCP(t, ag, doc(up), ctl.URL, work)

	// History and rollback (ADR-046): the deploy and the MCP
	// redeploy ran the same bundle, so a rollback with no ID has no target;
	// naming the first deployment rolls back through the whole stack.
	hr := ag.Exec("deployments", "hello-mcp", "--json")
	ok("deployments", hr)
	hist, _ := doc(hr)["deployments"].([]any)
	if len(hist) < 2 {
		t.Fatalf("tw deployments: %s", hr.Stdout)
	}
	if e, err := agenteval.CheckError(ag.Exec("rollback", "hello-mcp", "--json")); err != nil || e["code"] != "no_rollback_target" {
		t.Fatalf("rollback to the running bundle: %v %v", e, err)
	}
	oldest, _ := hist[len(hist)-1].(map[string]any)["deploymentID"].(string)
	rr := ag.Exec("rollback", "hello-mcp", oldest, "--json")
	ok("rollback", rr)
	if d := doc(rr); d["rollbackOf"] != oldest || d["status"] != "active" {
		t.Fatalf("tw rollback: %s", rr.Stdout)
	}
	if _, err := agenteval.CheckStream("deploy.event", rr.Stderr); err != nil {
		t.Fatalf("rollback events: %v", err)
	}

	// Preview and promote (ADR-048): a preview runs next to the
	// workload, through to ready, and promote makes its bundle the
	// workload's, through the whole stack.
	pv := ag.Exec("deploy", "--preview-alias", "try", "--json")
	ok("deploy", pv)
	if d := doc(pv); d["preview"] != "hello-mcp--preview-try" || d["workload"] != "hello-mcp" || d["status"] != "active" {
		t.Fatalf("tw deploy --preview: %s", pv.Stdout)
	}
	pl := ag.Exec("previews", "hello-mcp", "--json")
	ok("previews", pl)
	if ps, _ := doc(pl)["previews"].([]any); len(ps) != 1 || ps[0].(map[string]any)["alias"] != "try" {
		t.Fatalf("tw previews: %s", pl.Stdout)
	}
	pm := ag.Exec("promote", "hello-mcp", "try", "--json")
	ok("promote", pm)
	if d := doc(pm); d["preview"] != "hello-mcp--preview-try" || d["previewStopped"] != true || d["status"] != "active" {
		t.Fatalf("tw promote: %s", pm.Stdout)
	}
	if _, err := agenteval.CheckStream("deploy.event", pm.Stderr); err != nil {
		t.Fatalf("promote events: %v", err)
	}

	// Every other command once, with --json, against this tailworks.
	if _, err := agenteval.CheckLogs(ag.Exec("logs", "hello-mcp", "--console", "-n", "20", "--json").Stdout); err != nil {
		t.Fatal(err)
	}
	ok("version", ag.Exec("version", "--json"))
	if r := ag.Exec("doctor", "--json"); true {
		if err := agenteval.CheckDoc("doctor", doc(r)); err != nil {
			t.Fatal(err)
		}
		if !r.OK() {
			if _, err := agenteval.CheckError(agenteval.Run{Args: r.Args, Stderr: r.Stderr, Exit: r.Exit}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ok("telemetry", ag.Exec("telemetry", "--json"))
	ok("tenant ls", ag.Exec("tenant", "ls", "--json"))
	tenantID, _ := doc(up)["tenantID"].(string)
	ok("tenant set", ag.Exec("tenant", "set", tenantID, "--max-objects", "500", "--json"))
	ok("host ls", ag.Exec("host", "ls", "--json"))
	ok("host add", ag.Exec("host", "add", "tw-v-extra", "--out", filepath.Join(work, "extra.join"), "--json"))
	ok("host rm", ag.Exec("host", "rm", "tw-v-extra", "--yes", "--json")) // 204 from mgmt: nothing to clean up
	ag.Stdin = "not-a-real-secret"
	ok("secret put", ag.Exec("secret", "put", "API_TOKEN", "--json"))
	ok("secret ls", ag.Exec("secret", "ls", "--json"))
	ok("secret rm", ag.Exec("secret", "rm", "API_TOKEN", "--json"))
	okOrError("expose", ag.Exec("expose", "off", "hello-mcp", "--json"))
	ok("audit", ag.Exec("audit", "--all", "--json"))
	ok("image public ls", ag.Exec("image", "public", "ls", "--json"))
	ok("docs ls", ag.Exec("docs", "ls", "--json"))
	ok("docs search", ag.Exec("docs", "search", "acl_denied", "--json"))
	ok("docs show", ag.Exec("docs", "show", "errors.md#acl_denied", "--json"))
	ok("agent ls", ag.Exec("agent", "ls", "--json"))
	ok("agent show", ag.Exec("agent", "show", "tw-deploy", "--json"))
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	ag.Env = []string{"HOME=" + home}
	ok("agent setup", ag.Exec("agent", "setup", "--dry-run", "--json"))
	ok("agent remove", ag.Exec("agent", "remove", "--dry-run", "--json"))
	ag.Env = nil
	// kill: the workload's own object (tw ls lists it), then undeploy.
	var objID string
	for _, o := range doc(ag.Exec("ls", "--json"))["objects"].([]any) {
		if m := o.(map[string]any); m["workload"] == "hello-mcp" && m["parent"] == nil {
			objID, _ = m["objectID"].(string)
		}
	}
	if objID == "" {
		t.Fatal("tw ls: no object for hello-mcp")
	}
	ok("kill", ag.Exec("kill", objID, "--reason", "eval", "--json"))
	ok("audit", ag.Exec("audit", objID, "--json"))
	if _, err := agenteval.CheckError(ag.Exec("kill", objID, "--json")); err != nil {
		t.Fatal(err) // already killed: a typed error
	}

	// teardown: undeploy, as tw-teardown says.
	for _, r := range ag.Replay(steps["teardown"]) {
		ok("undeploy", r)
	}
	ok("down", ag.Exec("down", "--json"))

}

// testMCP is tailworks' MCP server (ADR-035) across processes: (a) the
// stdio bridge (`tw mcp`) on the tailworks host, as the operator: read tools by
// default, kill refused without --allow, a redeploy with --allow deploy
// audited as the operator via MCP; (b) a separate tailnet node holding
// only a read-only github.com/rosskukulinski/tailworks/cap/mcp grant, straight to mgmt's /mcp: read
// tools work, kill is cap_missing with the grant to add, /v1 is refused.
func testMCP(t *testing.T, ag *agenteval.Agent, up map[string]any, controlURL, work string) {
	t.Helper()
	rpc := func(id int, method string, params any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return string(b)
	}
	call := func(id int, tool string, args map[string]any) string {
		return rpc(id, "tools/call", map[string]any{"name": tool, "arguments": args})
	}
	bridge := func(args []string, msgs ...string) map[float64]map[string]any {
		t.Helper()
		ag.Stdin = strings.Join(msgs, "\n") + "\n"
		r := ag.Exec(append([]string{"mcp"}, args...)...)
		if !r.OK() {
			t.Fatalf("tw mcp: %s", r.Stderr)
		}
		out := map[float64]map[string]any{}
		ls, err := agenteval.Lines(r.Stdout)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range ls {
			out[l["id"].(float64)] = l
		}
		return out
	}
	result := func(m map[string]any) (map[string]any, map[string]any) {
		res, _ := m["result"].(map[string]any)
		sc, _ := res["structuredContent"].(map[string]any)
		if res["isError"] == true {
			return nil, sc["error"].(map[string]any)
		}
		return sc, nil
	}
	names := func(m map[string]any) []string {
		var out []string
		for _, x := range m["result"].(map[string]any)["tools"].([]any) {
			out = append(out, x.(map[string]any)["name"].(string))
		}
		return out
	}
	read := "whoami,status,ls,logs,audit"

	// (a) the bridge on the tailworks host.
	got := bridge(nil,
		rpc(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "eval", "version": "0"}}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		rpc(2, "tools/list", map[string]any{}),
		call(3, "ls", map[string]any{"workload": "hello-mcp"}),
		call(4, "kill", map[string]any{"object": "x", "reason": "eval"}))
	if len(got) != 4 || got[1]["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "tw" {
		t.Fatalf("tw mcp: %v", got)
	}
	if n := strings.Join(names(got[2]), ","); n != read {
		t.Fatalf("tw mcp tools: %s, want %s", n, read)
	}
	if ls, errv := result(got[3]); errv != nil || len(ls["deployments"].([]any)) != 1 {
		t.Fatalf("tw mcp ls: %v %v", ls, errv)
	}
	if _, errv := result(got[4]); errv == nil || errv["code"] != "cap_missing" || errv["snippet"] != "tw mcp --allow kill" {
		t.Fatalf("tw mcp kill without --allow: %v", errv)
	}
	got = bridge([]string{"--allow", "deploy"}, rpc(1, "tools/list", map[string]any{}), call(2, "deploy", map[string]any{"workload": "hello-mcp"}))
	if n := strings.Join(names(got[1]), ","); n != read+",deploy" {
		t.Fatalf("tw mcp --allow deploy tools: %s", n)
	}
	dep, errv := result(got[2])
	if errv != nil || dep["deploymentID"] == nil {
		t.Fatalf("tw mcp deploy (redeploy of the current bundle): %v %v", dep, errv)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		v, _ := agenteval.Doc(ag.Exec("ls", "--json"))
		var d map[string]any
		for _, x := range v.(map[string]any)["deployments"].([]any) {
			if m := x.(map[string]any); m["deploymentID"] == dep["deploymentID"] {
				d = m
			}
		}
		if d != nil && d["status"] == "active" {
			if !strings.Contains(fmt.Sprint(d["deployedBy"]), "(mcp)") {
				t.Fatalf("a deploy through MCP must be attributed to the caller via mcp: deployedBy %v", d["deployedBy"])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP redeploy never became active: %v", d)
		}
		time.Sleep(500 * time.Millisecond)
	}
	r := ag.Exec("mcp", "--config", "--json")
	if err := agenteval.CheckResult("mcp", r); err != nil {
		t.Fatal(err)
	}

	// (b) a tailnet agent with only the read-only MCP grant.
	n, err := nettest.NewNode(context.Background(), controlURL, nettest.DummyKey, "ttest-mcp-agent", filepath.Join(work, "mcp-agent"))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	hc := n.HTTPClient(30 * time.Second)
	base := "http://" + fmt.Sprint(up["mgmtIP"])
	post := func(body string) (int, map[string]any) {
		t.Helper()
		var resp *http.Response
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(time.Second) {
			resp, err = hc.Post(base+"/mcp", "application/json", strings.NewReader(body))
			if err == nil || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			t.Fatalf("POST /mcp from a tailnet node: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	if st, m := post(rpc(1, "tools/list", map[string]any{})); st != 200 || strings.Join(names(m), ",") != read {
		t.Fatalf("tailnet MCP tools/list: %d %v", st, m)
	}
	if _, m := post(call(2, "ls", map[string]any{})); true {
		if ls, errv := result(m); errv != nil || ls["tenantID"] != up["tenantID"] {
			t.Fatalf("tailnet MCP ls: %v %v", ls, errv)
		}
	}
	if _, m := post(call(3, "kill", map[string]any{"object": "x", "reason": "eval"})); true {
		if _, errv := result(m); errv == nil || errv["code"] != "cap_missing" || !strings.Contains(fmt.Sprint(errv["snippet"]), `"tools":["kill"]`) {
			t.Fatalf("tailnet MCP kill without the grant: %v", errv)
		}
	}
	resp, err := hc.Get(base + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("an MCP-only caller reached /v1/status: %d", resp.StatusCode)
	}
}
