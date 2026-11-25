package mgmt

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLogHost is a host push listener serving hostd's GET /v1/logs: it
// answers with lines naming the host and the query it got, and records
// the queries.
type fakeLogHost struct {
	mu      sync.Mutex
	queries []string
}

func (f *fakeLogHost) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func logHost(t *testing.T, e *testEnv, id string) *fakeLogHost {
	t.Helper()
	f := &fakeLogHost{}
	sock := filepath.Join(os.TempDir(), "tw-logs-"+id+"-"+filepath.Base(filepath.Dir(filepath.Dir(e.sock)))+".sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/logs" {
			w.WriteHeader(404)
			return
		}
		q := r.URL.Query()
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.RawQuery)
		f.mu.Unlock()
		if q.Get("workload") == "gone" {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"no_logs","message":"no log yet"}`))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s %s/%s n=%s\n", id, q.Get("tenant"), q.Get("workload"), q.Get("n"))
		if q.Get("follow") == "1" {
			w.(http.Flusher).Flush()
			fmt.Fprintf(w, "%s followed\n", id)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.Remove(sock) })
	if _, _, err := e.m.RegisterHost(context.Background(), RegisterRequest{HostID: id, CapacityNodes: 100, Tiers: []string{"process"}, PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

// getLogs is GET …/logs as the local admin, through the spec validator.
func getLogs(t *testing.T, e *testEnv, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://tw-mgmt"+path, nil)
	req.Header.Set(HostTokenHeader, e.token)
	res, err := e.c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestWorkloadLogs(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	ctx := context.Background()
	h1 := logHost(t, e, "h1")
	tnA := v1Tenant(t, e, "a.ts.net", nil)
	tnB := v1Tenant(t, e, "b.ts.net", nil)
	if _, err := e.store.AcquireLease(ctx, LeaseName(tnA, "web"), "h1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	logs := "/v1/tenants/" + tnA + "/workloads/web/logs"

	// One host: its text as is, with the tenant and workload from the path.
	st, body := getLogs(t, e, logs+"?n=7")
	if st != 200 || body != "h1 "+tnA+"/web n=7\n" {
		t.Fatalf("logs: %d %q", st, body)
	}
	if st, body := getLogs(t, e, logs+"?follow=true"); st != 200 || body != "h1 "+tnA+"/web n=50\nh1 followed\n" {
		t.Fatalf("follow: %d %q", st, body)
	}
	if q := h1.seen(); len(q) != 2 || !strings.Contains(q[1], "follow=1") {
		t.Fatalf("host queries %v", q)
	}

	// Another tenant's path never reaches tenant A's workload.
	e.c.fail("GET", "/v1/tenants/"+tnB+"/workloads/web/logs", nil, 404, "unknown_workload")
	e.c.fail("GET", "/v1/tenants/tn_nope/workloads/web/logs", nil, 404, "unknown_tenant")
	e.c.fail("GET", "/v1/tenants/"+tnA+"/workloads/other/logs", nil, 404, "unknown_workload")
	e.c.fail("GET", logs+"?host=h9", nil, 404, "unknown_workload")
	if st, eb := direct(t, e, "", "GET", logs+"?n=10001", nil); st != 400 || eb["code"] != "bad_request" {
		t.Fatalf("n over the cap: %d %v", st, eb)
	}
	if len(h1.seen()) != 2 {
		t.Fatalf("refused reads reached the host: %v", h1.seen())
	}

	// The host's typed refusal comes through; a dead host is 502.
	e.store.AcquireLease(ctx, LeaseName(tnA, "gone"), "h1", time.Minute, time.Now())
	e.c.fail("GET", "/v1/tenants/"+tnA+"/workloads/gone/logs", nil, 404, "no_logs")
	if _, _, err := e.m.RegisterHost(ctx, RegisterRequest{HostID: "hdead", CapacityNodes: 1, Tiers: []string{"process"}, PushURL: "unix:///nonexistent/tw.sock"}, "", ""); err != nil {
		t.Fatal(err)
	}
	e.store.AcquireLease(ctx, LeaseName(tnA, "dead"), "hdead", time.Minute, time.Now())
	e.c.fail("GET", "/v1/tenants/"+tnA+"/workloads/dead/logs", nil, 502, "host_unavailable")

	// Admin only: a host, a plain user and a tagged non-admin node are refused.
	srv := &Server{M: e.m}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case "100.64.0.1:1":
			return WhoIsInfo{NodeID: "nH1", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case "100.64.0.4:1":
			return WhoIsInfo{NodeID: "nEVE", Login: "eve@example.com"}, nil
		case "100.64.0.5:1":
			return WhoIsInfo{NodeID: "nAGENT", Tags: []string{"tag:tw-agent-support"}, Login: "tagged-devices"}, nil
		case "100.64.0.6:1":
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	h := srv.TailnetHandler(whois, srv.Handler())
	call := func(from string) (int, string) {
		r := httptest.NewRequest("GET", logs, nil)
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, from := range []string{"100.64.0.1:1", "100.64.0.4:1", "100.64.0.5:1"} {
		if c, b := call(from); c != 403 {
			t.Fatalf("non-admin %s: %d %s", from, c, b)
		}
	}
	if c, b := call("100.64.0.9:1"); c != 401 {
		t.Fatalf("no whois: %d %s", c, b)
	}
	if c, b := call("100.64.0.6:1"); c != 200 || b != "h1 "+tnA+"/web n=50\n" {
		t.Fatalf("admin via the cap: %d %q", c, b)
	}
	if len(h1.seen()) != 4 { // + no_logs and the admin's read
		t.Fatalf("refused callers reached the host: %v", h1.seen())
	}
	if !strings.Contains(e.logs.String(), "workload logs read") || !strings.Contains(e.logs.String(), "alice@example.com") {
		t.Fatal("the read is not logged with its actor")
	}
}

// A workload with replicas reads every replica's host, each line prefixed
// with the host; ?host= picks one.
func TestWorkloadLogsReplicas(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	logHost(t, e, "h1")
	logHost(t, e, "h2")
	jsonc := `{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"], "service": true } }`
	var dep DeploymentView
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "web",
		"bundle": base64.StdEncoding.EncodeToString(makeBundle(t, map[string]string{"tailworks.jsonc": jsonc, "dist/index.js": "export default {}"}))}, &dep)
	if len(dep.Replicas) != 2 {
		t.Fatalf("replicas %+v", dep.Replicas)
	}
	hostOf := map[string]string{}
	for _, r := range dep.Replicas {
		hostOf[r.Workload] = r.Host
	}
	logs := "/v1/tenants/" + tn + "/workloads/web/logs"
	st, body := getLogs(t, e, logs+"?n=5")
	want := fmt.Sprintf("[%[1]s] %[1]s %[3]s/web n=5\n[%[2]s] %[2]s %[3]s/web--r2 n=5\n", hostOf["web"], hostOf["web--r2"], tn)
	if st != 200 || body != want {
		t.Fatalf("replicas: %d %q, want %q", st, body, want)
	}
	h2 := hostOf["web--r2"]
	if st, body := getLogs(t, e, logs+"?host="+h2); st != 200 || body != h2+" "+tn+"/web--r2 n=50\n" {
		t.Fatalf("one replica by host: %d %q", st, body)
	}
	// Following several: every line prefixed, interleaved by line.
	st, body = getLogs(t, e, logs+"?follow=true&n=1")
	for _, h := range []string{hostOf["web"], h2} {
		if !strings.Contains(body, "["+h+"] "+h+" followed\n") {
			t.Fatalf("follow replicas: %d %q", st, body)
		}
	}
	// A replica name reads that replica alone.
	if st, body := getLogs(t, e, "/v1/tenants/"+tn+"/workloads/web--r2/logs?n=2"); st != 200 || body != h2+" "+tn+"/web--r2 n=2\n" {
		t.Fatalf("replica by name: %d %q", st, body)
	}
}
