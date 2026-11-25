package hostd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/obs"
)

// telHost is a Host with a telemetry pipeline (not started) and tenant
// tnA's workload "web".
func telHost(t *testing.T, opt TelemetryOptions) (*Host, *workload) {
	t.Helper()
	dir := t.TempDir()
	w := &workload{tenantID: "tnA", name: "web"}
	w.active = &instance{w: w, dir: dir}
	h := &Host{cfg: Config{HostID: "h1", DataDir: dir, Telemetry: opt}, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		workloads: map[string]*workload{"tnA/web": w}, objects: map[string]*object{}, byKey: map[string]*object{}}
	h.tel = newTelemetry(h)
	t.Cleanup(h.tel.close)
	return h, w
}

func records(t *testing.T, path string) []obs.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []obs.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r obs.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A workload over its record rate loses the excess (counted), and one
// "dropped" record per second says how many; another workload is not
// affected.
func TestRecordRateLimit(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{RecordRate: 1, RecordBurst: 5})
	other := &workload{tenantID: "tnA", name: "quiet"}
	for i := 0; i < 50; i++ {
		h.tel.emit(w, &obs.Record{Time: time.Now(), Kind: obs.KindRequest, Path: "/noisy"})
	}
	h.tel.emit(other, &obs.Record{Time: time.Now(), Kind: obs.KindRequest, Path: "/quiet"})
	h.tel.reportDrops(time.Now().Add(2 * time.Second))
	got := records(t, h.RequestLogPath("tnA", "web"))
	if len(got) != 6 || got[5].Kind != obs.KindDropped || got[5].Dropped != 45 {
		t.Fatalf("%d records, last %+v", len(got), got[len(got)-1])
	}
	if q := records(t, h.RequestLogPath("tnA", "quiet")); len(q) != 1 || q[0].Path != "/quiet" {
		t.Fatalf("quiet workload: %+v", q)
	}
	var b strings.Builder
	h.tel.reg.WritePrometheus(&b)
	if !strings.Contains(b.String(), `tw_logs_dropped_total{tw_tenant_id="tnA",tw_workload="web"} 45`) {
		t.Fatal(b.String())
	}
	// Per-record bounds hold before writing.
	big := &obs.Record{Time: time.Now(), Kind: obs.KindInvocation}
	for i := 0; i < 500; i++ {
		big.Logs = append(big.Logs, obs.LogLine{Message: strings.Repeat("x", 10000)})
	}
	h.tel.emit(other, big)
	q := records(t, h.RequestLogPath("tnA", "quiet"))
	if last := q[len(q)-1]; !last.Truncated || len(last.Logs) > obs.MaxLogLines {
		t.Fatalf("bound: %d lines truncated=%v", len(last.Logs), last.Truncated)
	}
}

// The request log rotates at RecordFileMax: at most two files.
func TestRecordLogRotates(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{RecordFileMax: 4 << 10, RecordRate: 1e6, RecordBurst: 1e6})
	for i := 0; i < 500; i++ {
		h.tel.emit(w, &obs.Record{Time: time.Now(), Kind: obs.KindRequest, Path: "/x", Logs: []obs.LogLine{{Message: strings.Repeat("y", 100)}}})
	}
	p := h.RequestLogPath("tnA", "web")
	matches, _ := filepath.Glob(p + "*")
	if len(matches) != 2 {
		t.Fatalf("files: %v", matches)
	}
	for _, m := range matches {
		if st, _ := os.Stat(m); st.Size() > 4<<10 {
			t.Fatalf("%s is %d bytes", m, st.Size())
		}
	}
}

// Ingress and tail halves join by span ID in either order; a half with no
// partner is written alone after JoinWait.
func TestJoin(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{JoinWait: time.Hour})
	in := func(span string) *obs.Record {
		return &obs.Record{Time: time.Now(), Kind: obs.KindRequest, SpanID: span, Status: 200, Caller: &obs.Caller{User: "ross@example.com"}}
	}
	tail := func(span string) *obs.Record {
		return &obs.Record{Time: time.Now(), Kind: obs.KindInvocation, SpanID: span, Outcome: "ok", Event: "fetch", Class: "Obj", Name: "a",
			Logs: []obs.LogLine{{Level: "log", Message: "hi " + span}}}
	}
	h.tel.join(w, in("s1"), true)
	h.tel.join(w, tail("s1"), false) // tail second
	h.tel.join(w, tail("s2"), false) // tail first
	h.tel.join(w, in("s2"), true)
	h.tel.join(w, in("s3"), true) // never joined
	got := records(t, h.RequestLogPath("tnA", "web"))
	if len(got) != 2 {
		t.Fatalf("%d records before the wait", len(got))
	}
	for _, r := range got {
		if r.Kind != obs.KindRequest || r.Outcome != "ok" || r.Caller == nil || len(r.Logs) != 1 || r.Logs[0].Message != "hi "+r.SpanID || r.Class != "Obj" {
			t.Fatalf("joined: %+v", r)
		}
	}
	h.tel.flushPending(time.Time{})
	got = records(t, h.RequestLogPath("tnA", "web"))
	if len(got) != 3 || got[2].SpanID != "s3" || got[2].Outcome != "" {
		t.Fatalf("after the wait: %+v", got)
	}
}

// A public request never adopts the caller's traceparent; a tailnet one
// does. The SERVER span's ID reaches workerd.
func TestIngressTraceContext(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{})
	inst := &instance{w: w, depID: "d1"}
	o := &object{ID: "o-1", TenantID: "tnA", Workload: "web"}
	audit := obs.NewTraceID()
	for _, c := range []struct {
		public bool
		trace  string
	}{{false, "4bf92f3577b34da6a3ce929d0e0e4736"}, {true, audit}} {
		r := httptest.NewRequest("GET", "/x?token=secret", nil)
		r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		blob := map[string]any{"tailscale": map[string]any{"caller": map[string]any{"node": "laptop.ts.net", "nodeId": "n1", "tags": []any{"tag:a"}}}}
		if c.public {
			blob = map[string]any{"tailscale": map[string]any{"requester": map[string]any{"kind": "public", "ip": "203.0.113.9"}}}
		}
		sp, rw := h.tel.startIngress(w, inst, o, r, blob, audit, httptest.NewRecorder())
		tc, ok := obs.ParseTraceparent(r.Header.Get("traceparent"))
		if !ok || tc.TraceID != c.trace || tc.SpanID != sp.rec.SpanID || tc.SpanID == "00f067aa0ba902b7" {
			t.Fatalf("public=%v: workerd got %q (record %+v)", c.public, r.Header.Get("traceparent"), sp.rec)
		}
		rw.WriteHeader(201)
		sp.end()
		if sp.rec.Status != 201 || sp.rec.Path != "/x" || (c.public != (sp.rec.Caller != nil && sp.rec.Caller.Public)) {
			t.Fatalf("record: %+v %+v", sp.rec, sp.rec.Caller)
		}
	}
}

// The raw console follow survives the capped log rotating under it.
func TestConsoleFollowAcrossRotation(t *testing.T) {
	t.Parallel()
	h, path := logsHost(t)
	rf, err := obs.OpenRotating(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	rf.Write([]byte("first\n"))
	srv := httptest.NewServer(http.HandlerFunc(h.logsHandler))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/logs?tenant=tnA&workload=web&n=5&follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	if l, _ := rd.ReadString('\n'); l != "first\n" {
		t.Fatalf("tail: %q", l)
	}
	// Enough to rotate twice.
	for i := 0; i < 6; i++ {
		rf.Write([]byte(strings.Repeat("z", 20) + "\n"))
	}
	rf.Write([]byte("after rotation\n"))
	for {
		l, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the line after rotation: %v", err)
		}
		if l == "after rotation\n" {
			return
		}
	}
}

// The export queue keeps the newest QueueMax items, whatever the mix of
// queued and new.
func TestExportQueueBound(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{QueueMax: 5})
	e := h.tel.exporterOf(w)
	e.known, e.cfg = true, &mgmtapi.OTLPExport{Endpoint: "http://otel:4318", Signals: obs.AllSignals}
	rec := func(n int) []obs.LogRecord {
		out := make([]obs.LogRecord, n)
		for i := range out {
			out[i].Body = strconv.Itoa(i)
		}
		return out
	}
	e.addLogs(rec(3))
	e.addLogs(rec(4)) // 7 > 5: the two oldest queued go
	if len(e.logs) != 5 || e.logs[0].Body != "2" || e.logs[1].Body != "0" || e.logs[4].Body != "3" {
		t.Fatalf("%+v", e.logs)
	}
	e.addLogs(rec(8)) // 13 > 5: all queued and three new go
	if len(e.logs) != 5 || e.logs[0].Body != "3" || e.logs[4].Body != "7" {
		t.Fatalf("%+v", e.logs)
	}
}
