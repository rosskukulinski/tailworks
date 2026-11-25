//go:build e2eobs

// Package observability is the hermetic logs/metrics/traces e2e
// (ADR-045): real tw-mgmt and tw-hostd processes on
// testcontrol + a local DERP, a tsnet caller, an echo node (an egress
// destination) and an OTLP collector node. The tenant's OTLP export is set
// through mgmt's API (custody) and released to the host as the workload's
// lease holder; the host exports from the workload's own node. Nothing
// talks to the internet; no real keys.
package observability

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const fakeToken = "fake-api-token"

// fakeTS mints keys testcontrol accepts and answers the policy reads.
func fakeTS(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" { // the OAuth client's exchange (mint mode oauth)
			json.NewEncoder(w).Encode(map[string]any{"access_token": fakeToken, "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != fakeToken {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
			mu.Lock()
			n++
			id := fmt.Sprintf("k%06d", n)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "fake-authkey-" + id})
		case r.Method == "DELETE":
			w.WriteHeader(200)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
			w.Header().Set("ETag", `"p1"`)
			w.Write([]byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
			json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

type env struct {
	t        *testing.T
	bin, dir string
	sock     string
	token    string
	hc       *http.Client
	procs    []*proc
}

func (e *env) start(name, bin string, args ...string) {
	e.t.Helper()
	lf, err := os.OpenFile(filepath.Join(e.dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(e.bin, bin), args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); lf.Close(); close(p.done) }()
	e.procs = append(e.procs, p)
}

func (e *env) stopAll() {
	for i := len(e.procs) - 1; i >= 0; i-- {
		p := e.procs[i]
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(15 * time.Second):
			syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	}
}

func (e *env) dumpLogs() {
	for _, name := range []string{"mgmt", "h1"} {
		b, _ := os.ReadFile(filepath.Join(e.dir, name+".log"))
		lines := strings.Split(string(b), "\n")
		if len(lines) > 80 {
			lines = lines[len(lines)-80:]
		}
		e.t.Logf("---- %s.log (tail)\n%s", name, strings.Join(lines, "\n"))
	}
}

// do calls mgmt's API on its local socket.
func (e *env) do(method, path string, body, out any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, "http://mgmt"+path, rd)
	rq.Header.Set(mgmt.HostTokenHeader, e.token)
	rq.Header.Set("content-type", "application/json")
	resp, err := e.hc.Do(rq)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		json.Unmarshal(b, out)
	}
	return resp.StatusCode, b
}

func (e *env) must(method, path string, body, out any) {
	e.t.Helper()
	if st, b := e.do(method, path, body, out); st/100 != 2 {
		e.dumpLogs()
		e.t.Fatalf("%s %s: %d %s", method, path, st, b)
	}
}

// hostGet reads hostd's local operator socket.
func (e *env) hostGet(path string) (int, string) {
	sock := filepath.Join(e.dir, "h1", "run", "hostd.sock")
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Get("http://hostd" + path)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type otlpPost struct {
	path, from, auth, body string
}

func waitFor(t *testing.T, e *env, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			e.dumpLogs()
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestObservabilityHermetic(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	dir, err := hostdtest.ShortDir("tw-obs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() || os.Getenv("TW_KEEP") == "" {
			os.RemoveAll(dir)
		}
	})
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	ts := fakeTS(t)
	e := &env{t: t, bin: cliflow.BuildBinaries(t), dir: dir, sock: filepath.Join(dir, "mgmt.sock")}
	t.Cleanup(e.stopAll)
	e.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.sock)
	}}}
	ctx := context.Background()

	// Tailnet peers: the caller, an echo node, the OTLP collector.
	var client, echo, otel *nettest.Node
	var wg sync.WaitGroup
	var errs [3]error
	for i, p := range []struct {
		n    **nettest.Node
		name string
	}{{&client, "ttest-client"}, {&echo, "ttest-echo"}, {&otel, "ttest-otel"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			*p.n, errs[i] = nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, p.name, filepath.Join(dir, p.name))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { client.Close(); echo.Close(); otel.Close() })
	if err := echo.ServeEcho(); err != nil {
		t.Fatal(err)
	}
	var cmu sync.Mutex
	var posts []otlpPost
	ln, err := otel.Listen("tcp", ":4318")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		from := ""
		if who, err := otel.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil {
			from = who.Node.Name
		}
		cmu.Lock()
		posts = append(posts, otlpPost{path: r.URL.Path, from: from, auth: r.Header.Get("Authorization"), body: string(b)})
		cmu.Unlock()
		io.WriteString(w, "{}")
	}))
	find := func(path string, want ...string) []otlpPost {
		cmu.Lock()
		defer cmu.Unlock()
		var out []otlpPost
		for _, p := range posts {
			ok := p.path == path
			for _, w := range want {
				ok = ok && strings.Contains(p.body, w)
			}
			if ok {
				out = append(out, p)
			}
		}
		return out
	}

	// mgmt and one host, as separate processes.
	keyFile := filepath.Join(dir, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	e.start("mgmt", "tw-mgmt", "-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"), "-rotate-host-token",
		"-state-dir", filepath.Join(dir, "mgmt"), "-tsapi-url", ts.URL, "-reap-interval", "1h")
	waitFor(t, e, "mgmt", 15*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "token"))
		if _, serr := os.Stat(e.sock); err == nil && len(bytes.TrimSpace(b)) == 64 && serr == nil {
			e.token = string(bytes.TrimSpace(b))
			return true
		}
		return false
	})
	var tn struct{ TenantID string }
	e.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	// The tenant's OTLP export, collector credential included (custody).
	e.must("PUT", "/v1/tenants/"+tn.TenantID+"/telemetry", map[string]any{"otlp": map[string]any{"endpoint": "http://ttest-otel:4318",
		"headers": map[string]string{"authorization": "Bearer e2e-t0ken"}}}, nil)
	hd := filepath.Join(dir, "h1")
	e.start("h1", "tw-hostd", "-local-dev", "-mgmt", "unix://"+e.sock, "-mgmt-token-file", filepath.Join(dir, "token"),
		"-host-id", "h1", "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
		"-control-url", ctl.URL, "-ephemeral", "-heartbeat", "500ms", "-sandbox", "none", "-workerd", wd, "-hostname-prefix", "h1-")
	waitFor(t, e, "host h1 live", 30*time.Second, func() bool {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		e.must("GET", "/v1/hosts", nil, &pg)
		return len(pg.Items) == 1 && pg.Items[0].Status == "live"
	})

	files, err := hostdtest.Build(hostdtest.Fixture("obsprobe"), map[string]string{"UPSTREAM": "http://ttest-echo/"})
	if err != nil {
		t.Fatal(err)
	}
	var dep struct {
		DeploymentID string
	}
	e.must("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "obsprobe", "bundle": base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))}, &dep)
	var d struct {
		Status, Error string
		Endpoint      struct{ Node, MagicDNS, Status string }
	}
	waitFor(t, e, "the deployment", 90*time.Second, func() bool {
		e.must("GET", "/v1/deployments/"+dep.DeploymentID, nil, &d)
		if d.Status == "failed" {
			t.Fatalf("deploy failed: %s", d.Error)
		}
		return d.Status == "active" && d.Endpoint.Status == "ready"
	})
	wlHost, _, _ := strings.Cut(d.Endpoint.MagicDNS, ".")
	hc := client.HTTPClient(30 * time.Second)

	// 1. traceparent: caller → Worker → DO (stub fetch) → egress, in
	// separate processes' telemetry pipelines: one trace, a span per hop.
	const t0, p0 = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	var r struct {
		Worker string
		Do     struct{ Traceparent, EchoTraceparent string }
	}
	waitFor(t, e, "the workload to answer", 60*time.Second, func() bool {
		rq, _ := http.NewRequest("GET", "http://"+d.Endpoint.MagicDNS+"/obj/alice", nil)
		rq.Header.Set("traceparent", "00-"+t0+"-"+p0+"-01")
		resp, err := hc.Do(rq)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&r) == nil
	})
	tw, ok1 := obs.ParseTraceparent(r.Worker)
	td, ok2 := obs.ParseTraceparent(r.Do.Traceparent)
	te, ok3 := obs.ParseTraceparent(r.Do.EchoTraceparent)
	if !ok1 || !ok2 || !ok3 || tw.TraceID != t0 || td.TraceID != t0 || te.TraceID != t0 || len(map[string]bool{p0: true, tw.SpanID: true, td.SpanID: true, te.SpanID: true}) != 4 {
		t.Fatalf("propagation: %+v", r)
	}
	t.Logf("TRACE %s: caller %s → ingress %s → DO call %s → egress %s", t0, p0, tw.SpanID, td.SpanID, te.SpanID)

	// 2. tw logs (the API a linked machine uses): mgmt reads the
	// records from the lease holder's hostd, filtered there.
	logs := func(q string) []obs.Record {
		rq, _ := http.NewRequest("GET", "http://mgmt/v1/tenants/"+tn.TenantID+"/workloads/obsprobe/logs?source=requests&n=50"+q, nil)
		rq.Header.Set(mgmt.HostTokenHeader, e.token)
		resp, err := e.hc.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("logs: %d %s", resp.StatusCode, b)
		}
		var out []obs.Record
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var rec obs.Record
			if json.Unmarshal(sc.Bytes(), &rec) == nil {
				out = append(out, rec)
			}
		}
		return out
	}
	var req, inv *obs.Record
	waitFor(t, e, "the request records via mgmt", 20*time.Second, func() bool {
		for _, rec := range logs("&trace=" + t0) {
			switch {
			case rec.Kind == obs.KindRequest && rec.SpanID == tw.SpanID:
				req = &rec
			case rec.Kind == obs.KindInvocation && rec.SpanID == td.SpanID:
				inv = &rec
			}
		}
		return req != nil && inv != nil
	})
	if req.Status != 200 || req.Caller == nil || !strings.HasPrefix(req.Caller.Node, "ttest-client.") || req.Outcome != "ok" || req.Host != "h1" || len(req.Logs) == 0 {
		t.Fatalf("request record: %+v", req)
	}
	if inv.Class != "Obj" || inv.Name != "alice" || len(inv.Logs) == 0 || inv.Logs[0].Message != "in Obj alice" {
		t.Fatalf("DO record: %+v", inv)
	}
	t.Logf("RECORD %s", strings.TrimSpace(req.Text()))
	t.Logf("RECORD %s", strings.TrimSpace(inv.Text()))
	if recs := logs("&errors=1"); len(recs) != 0 {
		t.Fatalf("errors filter: %+v", recs)
	}

	// 3. /metrics (hostd's operator socket; on a host with its own node
	// the same handler serves port 9464 of that node).
	st, body := e.hostGet("/metrics")
	for _, want := range []string{
		`http_server_request_duration_seconds_count{http_request_method="GET",http_response_status_code="200",tw_tenant_id="` + tn.TenantID + `",tw_workload="obsprobe"}`,
		`tw_invocations_total{tw_tenant_id="` + tn.TenantID + `",tw_workload="obsprobe",tw_object_class="Obj",tw_invocation_event="fetch",tw_invocation_outcome="ok"} 1`,
		`http_client_request_duration_seconds_count{http_request_method="GET",http_response_status_code="200",tw_tenant_id="` + tn.TenantID + `",tw_workload="obsprobe"`,
	} {
		if st != 200 || !strings.Contains(body, want) {
			t.Fatalf("/metrics lacks %s:\n%s", want, body)
		}
	}

	// 4. OTLP: logs, metrics and the span tree reach the collector, from
	// the workload's own node, with the tenant's credential.
	waitFor(t, e, "logs, metrics and traces at the collector", 30*time.Second, func() bool {
		return len(find("/v1/logs", "in Obj alice", t0)) > 0 && len(find("/v1/metrics", "http.server.request.duration", `"stringValue":"obsprobe"`)) > 0 &&
			len(find("/v1/traces", tw.SpanID, td.SpanID)) > 0 && len(find("/v1/traces", te.SpanID)) > 0
	})
	cmu.Lock()
	for _, p := range posts {
		if !strings.HasPrefix(p.from, wlHost+".") || p.auth != "Bearer e2e-t0ken" {
			cmu.Unlock()
			t.Fatalf("export from %q auth %q, want the workload node %s", p.from, p.auth, wlHost)
		}
	}
	n := len(posts)
	cmu.Unlock()
	t.Logf("OTLP: %d exports from %s (logs %d, metrics %d, traces %d)", n, wlHost, len(find("/v1/logs")), len(find("/v1/metrics")), len(find("/v1/traces")))
	for _, p := range find("/v1/traces", tw.SpanID) {
		if !strings.Contains(p.body, `"parentSpanId":"`+p0+`"`) {
			t.Fatalf("ingress span is not the caller's child: %s", p.body)
		}
	}
	// The release was audited, without the credential.
	var ap struct{ Items []json.RawMessage }
	e.must("GET", "/v1/audit?tenant="+tn.TenantID+"&kind=telemetry_release", nil, &ap)
	if len(ap.Items) == 0 || strings.Contains(fmt.Sprint(ap.Items), "e2e-t0ken") {
		t.Fatalf("telemetry_release audit: %s", ap.Items)
	}
}
