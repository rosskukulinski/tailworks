package mgmt

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeD1Host is a host push listener serving hostd's POST /v1/store-admin
// for D1: it records each call and answers like the platform would.
type fakeD1Host struct {
	mu    sync.Mutex
	calls []string // "<query string> <body>"
}

func (f *fakeD1Host) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func d1Host(t *testing.T, e *testEnv, id string) *fakeD1Host {
	t.Helper()
	f := &fakeD1Host{}
	sock := filepath.Join(os.TempDir(), "tw-d1-"+id+"-"+filepath.Base(filepath.Dir(filepath.Dir(e.sock)))+".sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/store-admin" {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.RawQuery+" "+string(b))
		f.mu.Unlock()
		switch {
		case q.Get("workload") == "gone":
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"unknown_workload","message":"not here"}`)
		case q.Get("name") == "nodb":
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"unknown_store","message":"the running deployment of web binds no d1 store \"nodb\""}`)
		case q.Get("action") == "export":
			io.WriteString(w, `{"sql":"PRAGMA defer_foreign_keys=TRUE;\nCREATE TABLE t (x);\n"}`)
		case strings.Contains(string(b), "FAIL"):
			io.WriteString(w, `{"success":false,"error":"near \"FAIL\": syntax error: SQLITE_ERROR"}`)
		case strings.Contains(string(b), "INSERT"):
			io.WriteString(w, `[{"success":true,"results":{"columns":[],"rows":[]},"meta":{"changes":1,"changed_db":true}}]`)
		default:
			io.WriteString(w, `[{"success":true,"results":{"columns":["n"],"rows":[[1]]},"meta":{"changes":0,"changed_db":false}}]`)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.Remove(sock) })
	if _, _, err := e.m.RegisterHost(context.Background(), RegisterRequest{HostID: id, CapacityNodes: 100, Tiers: []string{"process"}, PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestD1Routes: QueryD1 and ExportD1 reach the host holding the workload's
// lease with the tenant, workload and database from the path; SQL errors
// are 400 d1_error; a write is audited without its SQL; a read only
// logged; the host's typed refusals come through.
func TestD1Routes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	ctx := context.Background()
	h := d1Host(t, e, "h1")
	tn := v1Tenant(t, e, "a.ts.net", nil)
	tnB := v1Tenant(t, e, "b.ts.net", nil)
	for _, wl := range []string{"web", "gone"} {
		if _, err := e.store.AcquireLease(ctx, LeaseName(tn, wl), "h1", time.Minute, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/tenants/" + tn + "/workloads/web/d1/app-db"

	var rs []map[string]any
	e.c.ok("POST", base+"/query", map[string]any{"queries": []any{map[string]any{"sql": "SELECT 1 AS n"}}}, &rs)
	if len(rs) != 1 || rs[0]["success"] != true {
		t.Fatalf("read: %v", rs)
	}
	e.c.ok("POST", base+"/query", map[string]any{"queries": []any{map[string]any{"sql": "INSERT INTO t VALUES (?)", "params": []any{"secret value"}}}}, &rs)
	calls := h.seen()
	if len(calls) != 2 || !strings.Contains(calls[1], "action=query") || !strings.Contains(calls[1], "kind=d1") || !strings.Contains(calls[1], "name=app-db") ||
		!strings.Contains(calls[1], "tenant="+tn) || !strings.Contains(calls[1], "workload=web") || !strings.Contains(calls[1], `"secret value"`) {
		t.Fatalf("host calls: %v", calls)
	}
	recs := auditKinds(t, e, tn, "d1_write")
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"statements":1`) || strings.Contains(string(recs[0].Detail), "secret value") || strings.Contains(string(recs[0].Detail), "INSERT") {
		t.Fatalf("audit: %+v", recs)
	}
	if !strings.Contains(e.logs.String(), "d1 read") {
		t.Fatal("the read is not logged")
	}

	if eb := e.c.fail("POST", base+"/query", map[string]any{"queries": []any{map[string]any{"sql": "FAIL"}}}, 400, "d1_error"); !strings.Contains(eb["message"].(string), "syntax error") {
		t.Fatalf("d1_error: %v", eb)
	}
	if st, eb := direct(t, e, "", "POST", base+"/query", map[string]any{"queries": []any{}}); st != 400 || eb["code"] != "bad_request" {
		t.Fatalf("no statements: %d %v", st, eb)
	}
	e.c.fail("POST", "/v1/tenants/"+tn+"/workloads/web/d1/nodb/query", map[string]any{"queries": []any{map[string]any{"sql": "SELECT 1"}}}, 404, "unknown_store")
	e.c.fail("POST", "/v1/tenants/"+tn+"/workloads/gone/d1/app-db/query", map[string]any{"queries": []any{map[string]any{"sql": "SELECT 1"}}}, 404, "unknown_workload")
	e.c.fail("POST", "/v1/tenants/"+tn+"/workloads/idle/d1/app-db/query", map[string]any{"queries": []any{map[string]any{"sql": "SELECT 1"}}}, 404, "unknown_workload")
	e.c.fail("POST", "/v1/tenants/"+tnB+"/workloads/web/d1/app-db/query", map[string]any{"queries": []any{map[string]any{"sql": "SELECT 1"}}}, 404, "unknown_workload")
	n := len(h.seen())

	st, body := getLogs(t, e, base+"/export")
	if st != 200 || body != "PRAGMA defer_foreign_keys=TRUE;\nCREATE TABLE t (x);\n" {
		t.Fatalf("export: %d %q", st, body)
	}
	if got := h.seen(); len(got) != n+1 || !strings.Contains(got[n], "action=export") {
		t.Fatalf("export call: %v", got[n:])
	}
	var j map[string]any
	if json.Unmarshal([]byte(body), &j) == nil {
		t.Fatal("export is JSON, want SQL text")
	}
}
