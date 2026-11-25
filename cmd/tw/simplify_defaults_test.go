package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// fakeLinkedMgmt is a linked mgmt answering the routes the bare-default
// tests need; extra handles everything else. It returns the state dir.
func fakeLinkedMgmt(t *testing.T, extra http.HandlerFunc) string {
	t.Helper()
	page := func(items any) map[string]any { return map[string]any{"items": items, "nextCursor": nil} }
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v any
		switch r.URL.Path {
		case "/v1/whoami":
			v = map[string]any{"kind": "user", "login": "a@x", "admin": true, "actor": "a@x", "mgmt": map[string]any{}}
		case "/v1/tenants":
			v = page([]mgmt.Tenant{{ID: "tn_1", Tailnet: "t.ts.net", MintMode: "oauth"}})
		default:
			extra(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(api.Close)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-24T00:00:00Z"}`), 0o600)
	t.Cleanup(func() { jsonOut = false })
	return dir
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// `tw kill <parent>` cascades by default; --cascade=false (and the old
// --cascade) still work, and the CLI always says which it wants (#451).
func TestKillCascadesByDefault(t *testing.T) {
	var mu sync.Mutex
	var kills []string
	dir := fakeLinkedMgmt(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/objects":
			writeJSON(w, map[string]any{"items": []mgmt.Object{
				{ID: "obj_p", TenantID: "tn_1", Workload: "app", Namespace: "Agent", Name: "p", Status: "ready", RootID: "obj_p"},
				{ID: "obj_c", TenantID: "tn_1", Workload: "app", Namespace: "Agent", Name: "c", Status: "ready", RootID: "obj_p", ParentID: "obj_p"},
			}, "nextCursor": nil})
		case r.URL.Path == "/v1/audit":
			writeJSON(w, map[string]any{"items": []mgmt.AuditRecord{}, "nextCursor": nil})
		case r.Method == "DELETE" && r.URL.Path == "/v1/objects/obj_p":
			mu.Lock()
			kills = append(kills, r.URL.Query().Get("cascade"))
			mu.Unlock()
			killed := []string{"obj_p"}
			if r.URL.Query().Get("cascade") == "true" {
				killed = append(killed, "obj_c")
			}
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, mgmt.KillResult{Killed: killed, DevicesDeleted: []string{}})
		default:
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"code": "not_found", "message": r.URL.Path})
		}
	})
	for _, tc := range []struct {
		args    []string
		cascade string
		killed  int
	}{
		{nil, "true", 2},
		{[]string{"--cascade"}, "true", 2},
		{[]string{"--cascade=false"}, "false", 1},
	} {
		out, err := captureStdout(t, func() error {
			return cmdKill(append([]string{"--state-dir", dir, "--json", "obj_p"}, tc.args...))
		})
		jsonOut = false
		if err != nil {
			t.Fatalf("tw kill %v: %v", tc.args, err)
		}
		var kr killResult
		if err := json.Unmarshal([]byte(out), &kr); err != nil {
			t.Fatalf("tw kill %v --json: %v\n%s", tc.args, err, out)
		}
		mu.Lock()
		got := kills[len(kills)-1]
		mu.Unlock()
		if got != tc.cascade || len(kr.Result.Killed) != tc.killed || kr.Descendants != tc.killed-1 {
			t.Errorf("tw kill %v: sent cascade=%q, killed %v (descendants %d); want cascade=%s and %d killed", tc.args, got, kr.Result.Killed, kr.Descendants, tc.cascade, tc.killed)
		}
	}
}

// Bare `tw audit`: the newest -n records, oldest first, still verified;
// "older records" when more exist (#454).
func TestAuditBareNewest(t *testing.T) {
	var recs []mgmt.AuditRecord // oldest first
	for i := 1; i <= 7; i++ {
		recs = append(recs, mgmt.AuditRecord{Seq: int64(i), Chain: "mgmt", TS: fmt.Sprintf("2026-11-24T10:00:%02d.000Z", i), TenantID: "tn_1", Kind: "deploy", ActingFor: "a@x"})
	}
	var mu sync.Mutex
	var queries []string
	dir := fakeLinkedMgmt(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/objects":
			writeJSON(w, map[string]any{"items": []mgmt.Object{}, "nextCursor": nil})
		case "/v1/audit":
			q := r.URL.Query()
			mu.Lock()
			queries = append(queries, r.URL.RawQuery)
			mu.Unlock()
			// desc pages, the cursor an offset
			limit, _ := strconv.Atoi(q.Get("limit"))
			off, _ := strconv.Atoi(q.Get("cursor"))
			all := slices.Clone(recs)
			if q.Get("order") == "desc" {
				slices.Reverse(all)
			}
			end := min(off+limit, len(all))
			var next *string
			if end < len(all) {
				s := strconv.Itoa(end)
				next = &s
			}
			writeJSON(w, map[string]any{"items": all[off:end], "nextCursor": next, "chains": []mgmt.ChainStatus{{Chain: "mgmt", OK: true}}, "verified": true})
		default:
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"code": "not_found", "message": r.URL.Path})
		}
	})
	run := func(args ...string) (auditResult, string, error) {
		t.Helper()
		out, err := captureStdout(t, func() error { return cmdAudit(append([]string{"--state-dir", dir}, args...)) })
		jsonOut = false
		var res auditResult
		if slices.Contains(args, "--json") && err == nil {
			if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
				t.Fatalf("tw audit %v: %v\n%s", args, jerr, out)
			}
		}
		return res, out, err
	}
	seqs := func(res auditResult) (s []string) {
		for _, l := range res.Lines {
			s = append(s, l.TS[17:19])
		}
		return s
	}

	res, _, err := run("--json", "-n", "3")
	if err != nil || !res.Verified || !res.Truncated || !slices.Equal(seqs(res), []string{"05", "06", "07"}) {
		t.Fatalf("tw audit -n 3: %v %+v", err, res)
	}
	mu.Lock()
	if q := queries[len(queries)-1]; !strings.Contains(q, "order=desc") || !strings.Contains(q, "limit=3") || !strings.Contains(q, "tenant=tn_1") {
		t.Fatalf("bare audit query: %s", q)
	}
	mu.Unlock()
	// The default is 50: everything here, not truncated.
	res, _, err = run("--json")
	if err != nil || res.Truncated || len(res.Lines) != 7 || res.Lines[0].TS > res.Lines[6].TS {
		t.Fatalf("tw audit: %v %+v", err, res)
	}
	// Human: the footer and the pointer to the complete, verified listing.
	_, out, err := run("-n", "2")
	if err != nil || !strings.Contains(out, "2 records · chain verified\n") || !strings.Contains(out, "older records: tw audit --all") {
		t.Fatalf("tw audit -n 2: %v\n%s", err, out)
	}
	// --all stays complete whatever -n says.
	res, _, err = run("--json", "--all", "-n", "2")
	if err != nil || res.Truncated || len(res.Lines) != 7 {
		t.Fatalf("tw audit --all -n 2: %v %+v", err, res)
	}
	if _, _, err := run("-n", "0"); cliCode(err) != "bad_request" {
		t.Fatalf("tw audit -n 0: %v", err)
	}
}

// Bare `tw logs` on a linked machine: the app in the current directory,
// else the tenant's only deployed workload; never a daemon's log (#453).
func TestLogsBareDefaultRemote(t *testing.T) {
	var mu sync.Mutex
	deps := []mgmt.DeploymentView{
		{DeploymentID: "d1", TenantID: "tn_1", Workload: "web", Status: "active"},
		{DeploymentID: "d2", TenantID: "tn_1", Workload: "web--r2", Status: "active"},
		{DeploymentID: "d3", TenantID: "tn_1", Workload: "web--preview-try", Status: "active"},
		{DeploymentID: "d4", TenantID: "tn_1", Workload: "old", Status: "stopped"},
	}
	dir := fakeLinkedMgmt(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/deployments":
			mu.Lock()
			d := slices.Clone(deps)
			mu.Unlock()
			if r.URL.Query().Get("latest") != "true" || r.URL.Query().Get("tenant") != "tn_1" {
				t.Errorf("deployments query %s", r.URL.RawQuery)
			}
			writeJSON(w, map[string]any{"items": d, "nextCursor": nil})
		case strings.HasPrefix(r.URL.Path, "/v1/tenants/tn_1/workloads/"):
			wl := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tenants/tn_1/workloads/"), "/logs")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "%s line\n", wl)
		default:
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"code": "not_found", "message": r.URL.Path})
		}
	})
	t.Chdir(t.TempDir()) // no app here
	run := func(args ...string) (string, error) {
		t.Helper()
		return captureStdout(t, func() error { return cmdLogs(append([]string{"--state-dir", dir, "--console"}, args...)) })
	}
	// One workload (its replica and preview count as it): that one.
	if out, err := run(); err != nil || out != "web line\n" {
		t.Fatalf("bare tw logs, one workload: %q %v", out, err)
	}
	// Two: bad_request naming each, and the daemons' logs.
	mu.Lock()
	deps = append(deps, mgmt.DeploymentView{DeploymentID: "d5", TenantID: "tn_1", Workload: "api", Status: "active"})
	mu.Unlock()
	_, err := run()
	ce := asCLIErr(err)
	if ce.Code != "bad_request" || !slices.Contains(ce.Fix, "tw logs api") || !slices.Contains(ce.Fix, "tw logs web") ||
		!strings.Contains(strings.Join(ce.Fix, "\n"), "--component") {
		t.Fatalf("bare tw logs, two workloads: %+v", ce)
	}
	// The app in the current directory wins.
	app := t.TempDir()
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "api", "main": "src/index.ts"}`), 0o600)
	t.Chdir(app)
	if out, err := run(); err != nil || out != "api line\n" {
		t.Fatalf("bare tw logs in an app dir: %q %v", out, err)
	}
}

// Bare `tw logs` on the host: the only workload's log, not hostd.log;
// --component hostd still reads the daemon's log (#453).
func TestLogsBareDefaultLocal(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twld-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.WriteFile(filepath.Join(dir, "tw.json"), []byte(`{"tailnet":"t.ts.net","tenantID":"tn_1"}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
	os.WriteFile(filepath.Join(dir, "logs", "hostd.log"), []byte("hostd says hi\n"), 0o600)
	appLog := filepath.Join(dir, "workerd.log")
	os.WriteFile(appLog, []byte("web says hi\n"), 0o600)
	s := &state{Dir: dir}
	os.MkdirAll(s.hostdRunDir(), 0o700)
	ln, err := net.Listen("unix", s.hostdSock())
	if err != nil {
		t.Fatal(err)
	}
	workloads := []hostd.WorkloadStatus{
		{TenantID: "tn_1", Workload: "web", LogFile: appLog},
		{TenantID: "tn_other", Workload: "theirs", LogFile: appLog},
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hostd.StatusView{HostID: "h1", Workloads: workloads})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Chdir(t.TempDir())
	run := func(args ...string) (string, error) {
		t.Helper()
		return captureStdout(t, func() error { return cmdLogs(append([]string{"--state-dir", dir, "--console"}, args...)) })
	}
	if out, err := run(); err != nil || out != "web says hi\n" {
		t.Fatalf("bare tw logs on the host: %q %v", out, err)
	}
	if out, err := run("--component", "hostd"); err != nil || out != "hostd says hi\n" {
		t.Fatalf("tw logs --component hostd: %q %v", out, err)
	}
}

func TestBaseWorkloads(t *testing.T) {
	got := baseWorkloads([]string{"web--r3", "api", "web", "web--preview-abc", "api--r2", "x--r1"})
	if want := []string{"api", "web", "x--r1"}; !slices.Equal(got, want) {
		t.Fatalf("baseWorkloads: %v, want %v", got, want)
	}
}
