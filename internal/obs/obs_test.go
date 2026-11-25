package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTraceparent(t *testing.T) {
	ok := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc, valid := ParseTraceparent(ok)
	if !valid || tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || tc.SpanID != "00f067aa0ba902b7" || tc.Flags != "01" || tc.String() != ok {
		t.Fatalf("%+v %v", tc, valid)
	}
	// A future version is read by its 00 prefix (with more fields after).
	if _, valid := ParseTraceparent("cc-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"); !valid {
		t.Fatal("future version refused")
	}
	for _, bad := range []string{
		"", "garbage",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",   // zero trace
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",   // zero span
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",   // uppercase
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",   // version ff
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-x", // 00 with extra
		"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	} {
		if _, valid := ParseTraceparent(bad); valid {
			t.Errorf("accepted %q", bad)
		}
	}
	if !ValidTraceID(NewTraceID()) || !ValidSpanID(NewSpanID()) {
		t.Fatal("generated IDs invalid")
	}
}

func TestPrometheusExposition(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("http.server.request.duration", "s", "Duration of HTTP server requests.", []float64{0.1, 1}, "http.request.method", "http.response.status_code", "tw.workload")
	h.Observe(0.05, "GET", "200", "hello")
	h.Observe(0.1, "GET", "200", "hello") // le is inclusive
	h.Observe(3, "GET", "200", "hello")
	c := r.Counter("tw.invocations", "{invocation}", "Workerd invocations.", "tw.workload", "tw.invocation.outcome")
	c.Add(2, "hello", "ok")
	c.Add(1, "hello", `ex"cep\tion`)
	r.GaugeFunc("tw.host.nodes", "{node}", "", []string{"state"}, func(emit func(float64, ...string)) { emit(3, "ready") })
	var b bytes.Buffer
	if err := r.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE http_server_request_duration_seconds histogram",
		`http_server_request_duration_seconds_bucket{http_request_method="GET",http_response_status_code="200",tw_workload="hello",le="0.1"} 2`,
		`http_server_request_duration_seconds_bucket{http_request_method="GET",http_response_status_code="200",tw_workload="hello",le="1"} 2`,
		`http_server_request_duration_seconds_bucket{http_request_method="GET",http_response_status_code="200",tw_workload="hello",le="+Inf"} 3`,
		`http_server_request_duration_seconds_sum{http_request_method="GET",http_response_status_code="200",tw_workload="hello"} 3.15`,
		`http_server_request_duration_seconds_count{http_request_method="GET",http_response_status_code="200",tw_workload="hello"} 3`,
		"# TYPE tw_invocations_total counter",
		`tw_invocations_total{tw_workload="hello",tw_invocation_outcome="ok"} 2`,
		`tw_invocation_outcome="ex\"cep\\tion"`,
		`tw_host_nodes{state="ready"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The handler.
	rec := httptest.NewRecorder()
	MetricsHandler(r).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != PrometheusContentType || !strings.Contains(rec.Body.String(), "tw_invocations_total") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestSeriesLimitOverflow(t *testing.T) {
	r := NewRegistry()
	r.SetSeriesLimit(3)
	c := r.Counter("tw.egress", "", "", "tw.object.id")
	for i := 0; i < 10; i++ {
		c.Add(1, fmt.Sprintf("o-%d", i))
	}
	var b bytes.Buffer
	r.WritePrometheus(&b)
	if n := strings.Count(b.String(), "tw_egress_total{"); n != 4 {
		t.Fatalf("%d series (want 3 + overflow):\n%s", n, b.String())
	}
	if !strings.Contains(b.String(), `tw_egress_total{otel_metric_overflow="true"} 7`) {
		t.Fatalf("overflow:\n%s", b.String())
	}
	// A tenant-filtered snapshot never includes the shared overflow series.
	for _, m := range r.Snapshot(func(map[string]string) bool { return true }) {
		for _, p := range m.Points {
			if len(p.Attrs) == 1 && p.Attrs[0].Key == OverflowAttr {
				t.Fatal("overflow series in a filtered snapshot")
			}
		}
	}
	r.DeleteSeries(func(l map[string]string) bool { return l["tw.object.id"] == "o-0" })
	b.Reset()
	r.WritePrometheus(&b)
	if strings.Contains(b.String(), `"o-0"`) {
		t.Fatal("series not deleted")
	}
}

func TestPromName(t *testing.T) {
	for _, c := range []struct{ name, unit, kind, want string }{
		{"http.server.request.duration", "s", KindHistogram, "http_server_request_duration_seconds"},
		{"tw.invocations", "{invocation}", KindCounter, "tw_invocations_total"},
		{"tw.logs.dropped", "{record}", KindCounter, "tw_logs_dropped_total"},
		{"tw.host.memory", "By", KindGauge, "tw_host_memory_bytes"},
		{"tw.host.capacity.nodes", "1", KindGauge, "tw_host_capacity_nodes"},
	} {
		if got := PromName(c.name, c.unit, c.kind); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// TestOTLPPayloads checks the JSON encoding's field names and value
// forms against the OTLP specification (hex IDs, string int64s, enums as
// numbers), via a receiving test server.
func TestOTLPPayloads(t *testing.T) {
	got := map[string]map[string]any{}
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		got[r.URL.Path] = m
		hdr = r.Header
		w.WriteHeader(200)
		io.WriteString(w, "{}")
	}))
	defer srv.Close()
	c := &OTLPClient{Endpoint: srv.URL + "/base/", Headers: map[string]string{"Authorization": "Bearer s3cret"}}
	res := []Attr{A("service.name", "hello"), A("tw.tenant.id", "t1")}
	t0 := time.Unix(1700000000, 5)
	sp := Span{TraceID: NewTraceID(), SpanID: NewSpanID(), ParentSpanID: NewSpanID(), Name: "GET /", Kind: SpanKindServer, Start: t0, End: t0.Add(time.Millisecond),
		Attrs: []Attr{A("http.response.status_code", int64(200)), A("tw.caller.tags", []string{"tag:a"}), A("empty", "")}, Status: StatusError, StatusMessage: "boom"}
	ctx := context.Background()
	if err := c.Export(ctx, SignalTraces, TracesPayload(res, []Span{sp})); err != nil {
		t.Fatal(err)
	}
	if hdr.Get("Authorization") != "Bearer s3cret" || hdr.Get("Content-Type") != "application/json" {
		t.Fatal(hdr)
	}
	s := js(got["/base/v1/traces"])
	for _, want := range []string{`"traceId":"` + sp.TraceID + `"`, `"parentSpanId":"` + sp.ParentSpanID + `"`, `"kind":2`, `"startTimeUnixNano":"1700000000000000005"`,
		`{"key":"http.response.status_code","value":{"intValue":"200"}}`, `"arrayValue":{"values":[{"stringValue":"tag:a"}]}`, `"status":{"code":2,"message":"boom"}`,
		`{"key":"service.name","value":{"stringValue":"hello"}}`, `"scope":{"name":"tw"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("traces: missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"empty"`) {
		t.Error("empty attribute exported")
	}
	rec := &Record{Time: t0, Kind: KindRequest, Workload: "hello", Method: "GET", Path: "/x", Status: 500, TraceID: sp.TraceID, SpanID: sp.SpanID,
		Logs: []LogLine{{Time: t0, Level: "warn", Message: "careful"}}, Exceptions: []Exception{{Name: "Error", Message: "boom", Stack: "at x"}}}
	if err := c.Export(ctx, SignalLogs, LogsPayload(res, rec.LogRecords())); err != nil {
		t.Fatal(err)
	}
	l := js(got["/base/v1/logs"])
	for _, want := range []string{`"severityNumber":13`, `"severityText":"WARN"`, `"body":{"stringValue":"careful"}`, `"spanId":"` + sp.SpanID + `"`,
		`"severityNumber":17`, `"exception.stacktrace"`, `"stringValue":"tw.request"`, `"body":{"stringValue":"GET /x 500 0.0ms`} {
		if !strings.Contains(l, want) {
			t.Errorf("logs: missing %s in %s", want, l)
		}
	}
	reg := NewRegistry()
	reg.Histogram("http.server.request.duration", "s", "", []float64{0.1}, "tw.workload").Observe(0.2, "hello")
	reg.Counter("tw.invocations", "", "", "tw.workload").Add(1, "other")
	reg.GaugeFunc("tw.do.objects", "", "", []string{"tw.workload"}, func(emit func(float64, ...string)) { emit(4, "hello"); emit(9, "other") })
	snap := reg.Snapshot(func(l map[string]string) bool { return l["tw.workload"] == "hello" })
	if err := c.Export(ctx, SignalMetrics, MetricsPayload(res, snap)); err != nil {
		t.Fatal(err)
	}
	m := js(got["/base/v1/metrics"])
	for _, want := range []string{`"histogram":{"aggregationTemporality":2`, `"bucketCounts":["0","1"]`, `"explicitBounds":[0.1]`, `"count":"1"`, `"gauge":{"dataPoints":[{"asDouble":4`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics: missing %s in %s", want, m)
		}
	}
	if strings.Contains(m, "tw.invocations") || strings.Contains(m, `"asDouble":9`) {
		t.Errorf("another workload's series exported: %s", m)
	}
	// Errors: the status, never the headers.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusUnauthorized) }))
	defer bad.Close()
	err := (&OTLPClient{Endpoint: bad.URL, Headers: map[string]string{"Authorization": "Bearer s3cret"}}).Export(ctx, SignalLogs, LogsPayload(res, nil))
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestValidateEndpoint(t *testing.T) {
	for _, ok := range []string{"http://otel:4318", "https://otel.example.ts.net", "http://100.64.0.9:4318/otlp"} {
		if err := ValidateEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"otel:4318", "ftp://x", "http://", "http://u:p@otel", "http://otel?x=1", "http://otel#f"} {
		if err := ValidateEndpoint(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestRecordFilterTextBound(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 123e6, time.UTC)
	ok := &Record{Time: now, Kind: KindRequest, Object: "o-1", Class: "Counter", Name: "alice", Method: "GET", Path: "/hi", Status: 200, DurationMs: 12.34,
		Caller: &Caller{Node: "laptop.ttest.ts.net", User: "ross@example.com"}, TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", AuditTraceID: "aaaa",
		Logs: []LogLine{{Level: "log", Message: "hello\nworld"}}}
	bad := &Record{Time: now, Kind: KindInvocation, Event: "alarm", Class: "Counter", Outcome: "exception", Exceptions: []Exception{{Name: "Error", Message: "boom", Stack: "  at f (x.js:1)\n"}}}
	pub := &Record{Time: now, Kind: KindRequest, Method: "POST", Path: "/hook", Status: 503, Caller: &Caller{Public: true, IP: "203.0.113.9"}}
	cases := []struct {
		q    string
		want []*Record
	}{
		{"", []*Record{ok, bad, pub}},
		{"errors=1", []*Record{bad, pub}},
		{"status=5xx", []*Record{pub}},
		{"status=200", []*Record{ok}},
		{"outcome=exception", []*Record{bad}},
		{"object=Counter", []*Record{ok, bad}},
		{"object=Counter/alice", []*Record{ok}},
		{"object=o-1", []*Record{ok}},
		{"caller=ross", []*Record{ok}},
		{"caller=laptop", []*Record{ok}},
		{"caller=public", []*Record{pub}},
		{"trace=4bf92f35", []*Record{ok}},
		{"trace=aaaa", []*Record{ok}},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		f, err := ParseFilter(q)
		if err != nil {
			t.Fatal(c.q, err)
		}
		var got []*Record
		for _, r := range []*Record{ok, bad, pub} {
			if f.Match(r) {
				got = append(got, r)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: %d records, want %d", c.q, len(got), len(c.want))
		}
		if g, _ := ParseFilter(f.Values()); g != f {
			t.Errorf("%s: round trip %+v != %+v", c.q, g, f)
		}
	}
	if _, err := ParseFilter(url.Values{"status": {"6xx"}}); err == nil {
		t.Fatal("bad status accepted")
	}
	txt := ok.Text()
	for _, want := range []string{"2026-11-15T10:00:00.123Z GET /hi 200 12ms  Counter/alice  caller=ross@example.com  trace=4bf92f35", "    log   hello\n          world\n"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text %q lacks %q", txt, want)
		}
	}
	if txt := bad.Text(); !strings.Contains(txt, " alarm exception  Counter") || !strings.Contains(txt, "    error Error: boom\n          at f (x.js:1)\n") {
		t.Errorf("text %q", txt)
	}
	if !strings.Contains(pub.Text(), "caller=public:203.0.113.9") {
		t.Error(pub.Text())
	}
	// Bounds.
	big := &Record{}
	for i := 0; i < 200; i++ {
		big.Logs = append(big.Logs, LogLine{Message: strings.Repeat("é", 3000)})
	}
	for i := 0; i < 20; i++ {
		big.Exceptions = append(big.Exceptions, Exception{Stack: strings.Repeat("s", 10000)})
	}
	big.Bound()
	total := 0
	for _, l := range big.Logs {
		total += len(l.Message)
		if !utf8Valid(l.Message) || len(l.Message) > MaxLogLineBytes {
			t.Fatal("bad line bound")
		}
	}
	if !big.Truncated || total > MaxLogBytes || len(big.Logs) > MaxLogLines || len(big.Exceptions) != MaxExceptions || len(big.Exceptions[0].Stack) > MaxStackBytes {
		t.Fatalf("bound: %d lines %d bytes %d exceptions", len(big.Logs), total, len(big.Exceptions))
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestRotatingFileAndFollower(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.requests.jsonl")
	rf, err := OpenRotating(p, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	write := func(i int, status int) {
		b, _ := json.Marshal(&Record{Kind: KindRequest, Path: fmt.Sprintf("/%d", i), Status: status})
		rf.Write(append(b, '\n'))
	}
	for i := 0; i < 3; i++ {
		write(i, 200)
	}
	recs, size, err := TailRecords(p, 10, Filter{})
	if err != nil || len(recs) != 3 {
		t.Fatal(len(recs), err)
	}
	fl := NewRecordFollower(p, size)
	defer fl.Close()
	// Enough to rotate (300 bytes cap) more than once.
	for i := 3; i < 12; i++ {
		write(i, 200+i)
		got, err := fl.Next()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Path != fmt.Sprintf("/%d", i) {
			t.Fatalf("follow %d: %+v", i, got)
		}
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("never rotated")
	}
	st, _ := os.Stat(p)
	pst, _ := os.Stat(p + ".1")
	if st.Size() > 300 || pst.Size() > 300 {
		t.Fatalf("cap exceeded: %d %d", st.Size(), pst.Size())
	}
	// Tail reads across the rotated file, with a filter, oldest first.
	recs, _, _ = TailRecords(p, 2, Filter{Status: "2xx"})
	if len(recs) != 2 || recs[1].Path != "/11" {
		t.Fatalf("%+v", recs)
	}
	q, _ := ParseFilter(url.Values{"status": {"209"}})
	recs, _, _ = TailRecords(p, 5, q)
	if len(recs) != 1 || recs[0].Path != "/9" {
		t.Fatalf("filtered tail: %+v", recs)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(10, 5)
	now := time.Now()
	n := 0
	for i := 0; i < 20; i++ {
		if l.Allow(now) {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("burst %d", n)
	}
	if !l.Allow(now.Add(150*time.Millisecond)) || l.Allow(now.Add(150*time.Millisecond)) {
		t.Fatal("refill")
	}
}
