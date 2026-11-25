package hostd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// collector is an OTLP/HTTP receiver on its own tailnet node: it records
// every export with the exporting node (WhoIs) and the headers it carried.
type collector struct {
	node *nettest.Node
	mu   sync.Mutex
	got  []otlpPost
}

type otlpPost struct {
	path, from, auth string
	body             map[string]any
}

func startCollector(t *testing.T, s *suite) *collector {
	t.Helper()
	n, err := nettest.NewNode(context.Background(), s.ctl.URL, nettest.DummyKey, "ttest-otel", filepath.Join(s.e.Dir, "otel"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	c := &collector{node: n}
	ln, err := n.Listen("tcp", ":4318")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad body", 400)
			return
		}
		from := ""
		if who, err := n.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil && who.Node != nil {
			from = who.Node.Name
		}
		c.mu.Lock()
		c.got = append(c.got, otlpPost{path: r.URL.Path, from: from, auth: r.Header.Get("Authorization"), body: body})
		c.mu.Unlock()
		io.WriteString(w, "{}")
	}))
	return c
}

// find returns the posts to path whose JSON contains every want.
func (c *collector) find(path string, want ...string) []otlpPost {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []otlpPost
	for _, p := range c.got {
		if p.path != path {
			continue
		}
		s := js(p.body)
		ok := true
		for _, w := range want {
			if !strings.Contains(s, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// readRecords reads a request log.
func readRecords(t *testing.T, path string) []obs.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []obs.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r obs.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad record line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func hasLog(r obs.Record, msg string) bool {
	for _, l := range r.Logs {
		if strings.Contains(l.Message, msg) {
			return true
		}
	}
	return false
}

// TestTelemetry (ADR-045), hermetic: testcontrol + fakemgmt +
// hostd + stock workerd, a tsnet caller, an echo node and an OTLP
// collector node. Request records (ingress joined with workerd's tail),
// traceparent across a DO call and an egress fetch, /metrics, the logs
// API, and OTLP export from the workload's own node.
func TestTelemetry(t *testing.T) {
	t.Parallel()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.Telemetry = hostd.TelemetryOptions{JoinWait: time.Second, ExportEvery: 200 * time.Millisecond, MetricsEvery: 300 * time.Millisecond, RefreshEvery: 500 * time.Millisecond}
	})
	e := s.e
	coll := startCollector(t, s)
	e.FM.SetTelemetry(e.TenantID, &mgmtapi.OTLPExport{Endpoint: "http://ttest-otel:4318", Headers: map[string]string{"authorization": "Bearer t0ken"}, Signals: obs.AllSignals})
	dep := e.Deploy(hostdtest.Fixture("obsprobe"), "obsprobe", map[string]string{"UPSTREAM": "http://ttest-echo/"})
	wl := e.WaitActive("obsprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	waitFor(t, "the OTLP config", 20*time.Second, func() bool { return e.Host.TelemetryExporting(e.TenantID, "obsprobe") })
	var logPath string
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "obsprobe" {
			logPath = w.RequestLog
		}
	}
	if logPath == "" {
		t.Fatal("no RequestLog in the workload status")
	}
	base := "http://" + wl.IP
	const t0, p0 = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	in := "00-" + t0 + "-" + p0 + "-01"

	// One request: Worker → facet DO (stub fetch) → egress (echo).
	var tpWorker, tpDO, tpEcho obs.TraceContext
	t.Run("TraceparentAcrossDOAndEgress", func(t *testing.T) {
		code, r := s.getJSON(base+"/obj/alice", "traceparent", in)
		if code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		var ok bool
		w, _ := r["worker"].(string)
		d, _ := get(r, "do", "traceparent").(string)
		ec, _ := get(r, "do", "echoTraceparent").(string)
		if tpWorker, ok = obs.ParseTraceparent(w); !ok {
			t.Fatalf("worker saw no traceparent: %v", r)
		}
		if tpDO, ok = obs.ParseTraceparent(d); !ok {
			t.Fatalf("DO saw no traceparent: %v", r)
		}
		if tpEcho, ok = obs.ParseTraceparent(ec); !ok {
			t.Fatalf("the egress destination saw no traceparent: %v", r)
		}
		// One trace (the caller's), a new span at each hop.
		for _, tc := range []obs.TraceContext{tpWorker, tpDO, tpEcho} {
			if tc.TraceID != t0 {
				t.Fatalf("trace not propagated: %v", r)
			}
		}
		if tpWorker.SpanID == p0 || tpDO.SpanID == tpWorker.SpanID || tpEcho.SpanID == tpDO.SpanID || tpEcho.SpanID == tpWorker.SpanID {
			t.Fatalf("hops share a span: worker %s do %s echo %s (caller %s)", tpWorker.SpanID, tpDO.SpanID, tpEcho.SpanID, p0)
		}
	})

	t.Run("RecordsJoinIngressAndTail", func(t *testing.T) {
		var req, inv *obs.Record
		waitFor(t, "the request and the DO invocation records", 15*time.Second, func() bool {
			req, inv = nil, nil
			for _, r := range readRecords(t, logPath) {
				if r.Kind == obs.KindRequest && r.SpanID == tpWorker.SpanID {
					req = &r
				}
				if r.Kind == obs.KindInvocation && r.SpanID == tpDO.SpanID {
					inv = &r
				}
			}
			return req != nil && inv != nil
		})
		// The request: hostd's half (verified caller, status, duration)
		// joined with workerd's (console lines, outcome).
		if req.TraceID != t0 || req.ParentSpanID != p0 || req.Status != 200 || req.DurationMs <= 0 || req.Method != "GET" || req.Path != "/obj/alice" ||
			req.Outcome != "ok" || req.Event != "fetch" || req.Workload != "obsprobe" || req.Tenant != e.TenantID || req.Object != wl.ID || req.AuditTraceID == "" {
			t.Fatalf("request record: %+v", req)
		}
		if req.Caller == nil || req.Caller.NodeID != s.clientID || !strings.HasPrefix(req.Caller.Node, "ttest-client.") {
			t.Fatalf("caller: %+v", req.Caller)
		}
		if !hasLog(*req, "calling obj alice") {
			t.Fatalf("worker console line missing: %+v", req.Logs)
		}
		// The DO's invocation, attested as Obj/alice (a facet).
		o := e.WaitObject("obsprobe", "Obj", "alice", 30*time.Second)
		if inv.Class != "Obj" || inv.Name != "alice" || inv.Object != o.ID || inv.TraceID != t0 || inv.Outcome != "ok" || !hasLog(*inv, "in Obj alice") {
			t.Fatalf("DO invocation record: %+v", inv)
		}
	})

	t.Run("PlainDOAndText", func(t *testing.T) {
		code, r := s.getJSON(base+"/plain/bob", "traceparent", in)
		d, _ := get(r, "do", "traceparent").(string)
		ec, _ := get(r, "do", "echoTraceparent").(string)
		tcD, ok1 := obs.ParseTraceparent(d)
		tcE, ok2 := obs.ParseTraceparent(ec)
		if code != 200 || !ok1 || !ok2 || tcD.TraceID != t0 || tcE.TraceID != t0 || tcE.SpanID == tcD.SpanID {
			t.Fatalf("plain DO: %d %v", code, r)
		}
		var inv *obs.Record
		waitFor(t, "the plain DO's record", 15*time.Second, func() bool {
			for _, r := range readRecords(t, logPath) {
				if r.Kind == obs.KindInvocation && r.SpanID == tcD.SpanID {
					inv = &r
					return true
				}
			}
			return false
		})
		if inv.Class != "Plain" || inv.DurableObjectID == "" || !hasLog(*inv, "in Plain bob") || inv.Logs[0].Level != "warn" {
			t.Fatalf("plain DO record: %+v", inv)
		}
		if txt := inv.Text(); !strings.Contains(txt, "fetch GET /bob ok  Plain") || !strings.Contains(txt, "    warn  in Plain bob") {
			t.Fatalf("text: %q", txt)
		}
	})

	t.Run("ExceptionAndNoQueryString", func(t *testing.T) {
		if code, _, _ := s.do("GET", base+"/throw", nil); code != 500 {
			t.Fatalf("/throw: %d", code)
		}
		if code, _, _ := s.do("GET", base+"/hi?token=s3cr3t-value", nil); code != 200 {
			t.Fatalf("/hi: %d", code)
		}
		var thrown, hi *obs.Record
		waitFor(t, "the /throw and /hi records", 15*time.Second, func() bool {
			for _, r := range readRecords(t, logPath) {
				switch r.Path {
				case "/throw":
					thrown = &r
				case "/hi":
					hi = &r
				}
			}
			return thrown != nil && hi != nil && thrown.Outcome != ""
		})
		if thrown.Status != 500 || thrown.Outcome != "exception" || len(thrown.Exceptions) != 1 || thrown.Exceptions[0].Message != "worker boom" ||
			!hasLog(*thrown, "about to throw") || thrown.Logs[0].Level != "error" || !thrown.IsError() {
			t.Fatalf("exception record: %+v", thrown)
		}
		// No caller traceparent: the trace is the hop's audit trace.
		if hi.TraceID != hi.AuditTraceID || hi.ParentSpanID != "" {
			t.Fatalf("root trace: %+v", hi)
		}
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), "ticket") {
			t.Fatal("the request log holds a query string or a create ticket")
		}
	})

	t.Run("LogsAPI", func(t *testing.T) {
		// GET /v1/logs?source=requests on the push API (what mgmt reads
		// for a linked machine): filtered on the host.
		rec := httptest.NewRecorder()
		e.Host.PushHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/logs?tenant="+e.TenantID+"&workload=obsprobe&source=requests&errors=1&n=10", nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		n := 0
		for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
			var r obs.Record
			if err := json.Unmarshal([]byte(line), &r); err != nil || !r.IsError() {
				t.Fatalf("not an error record: %s", line)
			}
			n++
		}
		if n == 0 {
			t.Fatal("no error records")
		}
		rec = httptest.NewRecorder()
		e.Host.PushHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/logs?tenant="+e.TenantID+"&workload=obsprobe&source=requests&trace="+t0[:8]+"&object=Obj/alice", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"alice"`) || strings.Contains(rec.Body.String(), `"path":"/hi"`) {
			t.Fatalf("trace+object filter: %d %s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		e.Host.PushHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/logs?tenant="+e.TenantID+"&workload=obsprobe&source=requests&status=6xx", nil))
		if rec.Code != 400 {
			t.Fatalf("bad filter: %d", rec.Code)
		}
		// The console is still there.
		rec = httptest.NewRecorder()
		e.Host.PushHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/logs?tenant="+e.TenantID+"&workload=obsprobe&n=200", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "in Obj alice") {
			t.Fatalf("console: %d %s", rec.Code, hostdtest.Trim(rec.Body.String(), 400))
		}
	})

	t.Run("PrometheusScrape", func(t *testing.T) {
		rec := httptest.NewRecorder()
		e.Host.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		body := rec.Body.String()
		if rec.Code != 200 || rec.Header().Get("Content-Type") != obs.PrometheusContentType {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		for _, want := range []string{
			`http_server_request_duration_seconds_count{http_request_method="GET",http_response_status_code="200",tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe"}`,
			`http_server_request_duration_seconds_count{http_request_method="GET",http_response_status_code="500",tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe"} 1`,
			`tw_invocations_total{tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe",tw_object_class="Obj",tw_invocation_event="fetch",tw_invocation_outcome="ok"}`,
			`tw_invocation_outcome="exception"`,
			`http_client_request_duration_seconds_bucket{http_request_method="GET",http_response_status_code="200",tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe"`,
			`tw_do_objects{tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe",tw_object_class="Obj",tw_object_status="ready"} 1`,
			`tw_do_wakeups_total{tw_tenant_id="` + e.TenantID + `",tw_workload="obsprobe",tw_object_class="Obj"}`,
			`tw_host_capacity{tw_resource="nodes"}`,
			`tw_workloads{tw_tenant_id="` + e.TenantID + `",tw_workload_status="active"}`,
			"# TYPE tw_logs_dropped_total counter",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("/metrics lacks %s", want)
			}
		}
		if t.Failed() {
			t.Log(body)
		}
	})

	t.Run("OTLPExportFromTheWorkloadNode", func(t *testing.T) {
		waitFor(t, "logs, spans and metrics at the collector", 20*time.Second, func() bool {
			return len(coll.find("/v1/logs", `"in Obj alice"`, t0)) > 0 &&
				len(coll.find("/v1/traces", tpWorker.SpanID, tpDO.SpanID)) > 0 &&
				len(coll.find("/v1/traces", tpEcho.SpanID)) > 0 &&
				len(coll.find("/v1/metrics", `"http.server.request.duration"`)) > 0
		})
		coll.mu.Lock()
		posts := append([]otlpPost(nil), coll.got...)
		coll.mu.Unlock()
		for _, p := range posts {
			// Dialed from the workload's own node, with the tenant's header.
			if !strings.HasPrefix(p.from, wl.Hostname+".") || p.auth != "Bearer t0ken" {
				t.Fatalf("export from %q with auth %q (want the workload node %s)", p.from, p.auth, wl.Hostname)
			}
			if !strings.Contains(js(p.body), `{"key":"service.name","value":{"stringValue":"obsprobe"}}`) {
				t.Fatalf("resource: %s", hostdtest.Trim(js(p.body), 300))
			}
		}
		// The spans form one tree: SERVER (hostd ingress, parent = the
		// caller's span) → CLIENT "Obj fetch" (runtime) → CLIENT (egress).
		spans := map[string]map[string]any{}
		for _, p := range coll.find("/v1/traces") {
			for _, rs := range p.body["resourceSpans"].([]any) {
				for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
					for _, sp := range ss.(map[string]any)["spans"].([]any) {
						m := sp.(map[string]any)
						spans[m["spanId"].(string)] = m
					}
				}
			}
		}
		chk := func(id, parent string, kind float64, name string) {
			t.Helper()
			sp := spans[id]
			if sp == nil {
				t.Fatalf("span %s not exported", id)
			}
			pid, _ := sp["parentSpanId"].(string)
			if sp["traceId"] != t0 || pid != parent || sp["kind"] != kind || (name != "" && sp["name"] != name) {
				t.Fatalf("span %s: %v (want parent %s kind %v name %q)", id, sp, parent, kind, name)
			}
		}
		chk(tpWorker.SpanID, p0, obs.SpanKindServer, "GET obsprobe")
		chk(tpDO.SpanID, tpWorker.SpanID, obs.SpanKindClient, "Obj fetch")
		chk(tpEcho.SpanID, tpDO.SpanID, obs.SpanKindClient, "GET")
		// Metrics are the workload's only.
		for _, p := range coll.find("/v1/metrics") {
			if strings.Contains(js(p.body), "tw.host.capacity") {
				t.Fatal("host-level metrics exported to a tenant")
			}
		}
	})

	// After PrometheusScrape, which counts the Obj objects.
	t.Run("EntryTraces", func(t *testing.T) { testEntryTraces(t, s, coll, logPath, base, in, t0) }) // telemetry_entries_test.go

	t.Run("ExportStopsWhenUnset", func(t *testing.T) {
		e.FM.SetTelemetry(e.TenantID, nil)
		waitFor(t, "the export to stop", 10*time.Second, func() bool { return !e.Host.TelemetryExporting(e.TenantID, "obsprobe") })
	})
}
