package hostd

// Observability (ADR-045), host side.
//
//   - Ingress: every request through ingressHandler gets a SERVER span. A
//     tailnet caller's traceparent is honoured (observability only: the
//     audit trace ID stays hostd's); workerd receives traceparent with
//     hostd's span as the parent.
//   - Egress: every request through serveEgress gets a CLIENT span, child
//     of the runtime's context, and the next hop receives it as parent.
//   - workerd's tail (platform Tail entrypoint → POST /v1/tail on the
//     instance's host API) supplies console lines, exceptions and the
//     outcome; the runtime's DO-call spans arrive on POST /v1/spans.
//   - The ingress half and the tail half of a request are joined by span ID
//     (at most JoinWait apart) into one obs.Record, written to the
//     workload's request log (rate-limited, rotating) and queued for the
//     tenant's OTLP export, if it has one.
//   - Metrics: one registry per host, served on GET /metrics (the host's
//     own tailnet node; MetricsListener) and exported per workload.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/obs"
)

// TelemetryOptions tune the pipeline (zero values: the defaults).
type TelemetryOptions struct {
	// JoinWait is how long the ingress half of a request waits for its
	// workerd half (and vice versa) before it is written alone (2 s).
	JoinWait time.Duration
	// RecordRate/RecordBurst bound the records a workload writes to its
	// request log (200/s, burst 1000); the rest are dropped and counted.
	RecordRate, RecordBurst float64
	// RecordFileMax is where a request log rotates (8 MiB; one previous
	// file is kept). AppLogMax is the same for workerd.log / app.log (16 MiB).
	RecordFileMax, AppLogMax int64
	// ExportEvery is how often queued logs and spans are exported (2 s);
	// MetricsEvery how often a workload's metrics are (30 s); RefreshEvery
	// how often a workload's OTLP config is re-read from mgmt (60 s).
	ExportEvery, MetricsEvery, RefreshEvery time.Duration
	// QueueMax bounds each workload's export queue per signal (4096).
	QueueMax int

	// RecordMaxAge also drops request records older than this
	// (telemetry_retention.go; 0 = only the size rotation, the default).
	RecordMaxAge time.Duration
}

func (o TelemetryOptions) withDefaults() TelemetryOptions {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.JoinWait, 2*time.Second)
	def(&o.ExportEvery, 2*time.Second)
	def(&o.MetricsEvery, 30*time.Second)
	def(&o.RefreshEvery, 60*time.Second)
	if o.RecordRate <= 0 {
		o.RecordRate = 200
	}
	if o.RecordBurst <= 0 {
		o.RecordBurst = 1000
	}
	if o.RecordFileMax <= 0 {
		o.RecordFileMax = 8 << 20
	}
	if o.AppLogMax <= 0 {
		o.AppLogMax = 16 << 20
	}
	if o.QueueMax <= 0 {
		o.QueueMax = 4096
	}
	return o
}

// Metric attribute keys.
const (
	attrTenant   = "tw.tenant.id"
	attrWorkload = "tw.workload"
	attrClass    = "tw.object.class"
	attrObject   = "tw.object.id"
)

type telemetry struct {
	h   *Host
	opt TelemetryOptions
	reg *obs.Registry

	reqDur      *obs.Histogram
	egressDur   *obs.Histogram
	invocations *obs.Counter
	wakeups     *obs.Counter
	logsDropped *obs.Counter
	expDropped  *obs.Counter
	expErrors   *obs.Counter

	mu      sync.Mutex
	pending map[string]*pendingHalf // span ID → the half waiting for the other
	logs    map[string]*recordLog   // tenant/workload
	exps    map[string]*exporter    // tenant/workload

	// entries joins DO RPC and alarm records with their runtime entry
	// spans (telemetry_entries.go); made on first use.
	entries *entryJoin

	stop chan struct{}
	wg   sync.WaitGroup
}

type pendingHalf struct {
	w        *workload
	rec      *obs.Record
	ingress  bool
	deadline time.Time
}

type recordLog struct {
	f          *obs.RotatingFile
	lim        *obs.Limiter
	dropped    int
	lastReport time.Time
}

// maxPending bounds the join table: beyond it, halves are written alone.
const maxPending = 20000

func newTelemetry(h *Host) *telemetry {
	t := &telemetry{h: h, opt: h.cfg.Telemetry.withDefaults(), reg: obs.NewRegistry(),
		pending: map[string]*pendingHalf{}, logs: map[string]*recordLog{}, exps: map[string]*exporter{}, stop: make(chan struct{})}
	r := t.reg
	t.reqDur = r.Histogram("http.server.request.duration", "s", "Duration of requests to workloads and Durable Objects, through hostd's ingress (any tier).",
		obs.DurationBuckets, "http.request.method", "http.response.status_code", attrTenant, attrWorkload, attrClass)
	t.egressDur = r.Histogram("http.client.request.duration", "s", "Duration of egress requests, per object (refusals have error.type).",
		obs.DurationBuckets, "http.request.method", "http.response.status_code", "error.type", attrTenant, attrWorkload, attrObject)
	t.invocations = r.Counter("tw.invocations", "{invocation}", "workerd invocations by event and outcome (from the tail).",
		attrTenant, attrWorkload, attrClass, "tw.invocation.event", "tw.invocation.outcome")
	t.wakeups = r.Counter("tw.do.wakeups", "{wakeup}", "Durable Object instances started (constructed) in workerd.", attrTenant, attrWorkload, attrClass)
	t.logsDropped = r.Counter("tw.logs.dropped", "{record}", "Request records not written: the workload exceeded its record rate.", attrTenant, attrWorkload)
	t.expDropped = r.Counter("tw.telemetry.export.dropped", "{item}", "Telemetry items not exported (queue full, or the export failed).", attrTenant, attrWorkload, "tw.signal")
	t.expErrors = r.Counter("tw.telemetry.export.errors", "{export}", "Failed OTLP exports.", attrTenant, attrWorkload, "tw.signal")
	r.GaugeFunc("tw.do.objects", "{object}", "Objects (Durable Object instances with a tailnet node, and workload entry nodes) on this host, by status.",
		[]string{attrTenant, attrWorkload, attrClass, "tw.object.status"}, t.objectCounts)
	r.GaugeFunc("tw.host.capacity", "1", "This host's capacity vector (nodes, vms, vm_memory_mib, huge_memory_mib, vcpus, scratch_mib).",
		[]string{"tw.resource"}, t.capacity)
	r.GaugeFunc("tw.host.usage", "1", "This host's live usage against its capacity (same resources, plus containers).",
		[]string{"tw.resource"}, t.usage)
	r.GaugeFunc("tw.workloads", "{workload}", "Workloads on this host by status.", []string{attrTenant, "tw.workload.status"}, t.workloadCounts)
	return t
}

func (t *telemetry) start() {
	t.wg.Add(1)
	go t.loop()
	t.startRecordPruner() // telemetry_retention.go
}

func (t *telemetry) close() {
	select {
	case <-t.stop:
		return
	default:
	}
	close(t.stop)
	t.wg.Wait()
	t.flushPending(time.Time{}) // everything still waiting is written alone
	t.mu.Lock()
	for _, l := range t.logs {
		l.f.Close()
	}
	t.mu.Unlock()
}

// ---- metrics ----

// Metrics is the host's registry (for a combined /metrics in mgmt's
// process, single-process tw up).
func (h *Host) Metrics() *obs.Registry { return h.tel.reg }

// MetricsHandler serves GET /metrics.
func (h *Host) MetricsHandler() http.Handler {
	if h.tel == nil {
		return http.NotFoundHandler()
	}
	return obs.MetricsHandler(h.tel.reg)
}

func (t *telemetry) objectCounts(emit func(float64, ...string)) {
	type k struct{ tenant, wl, class, status string }
	n := map[k]int{}
	t.h.mu.Lock()
	for _, o := range t.h.objects {
		if o.isPool() {
			continue
		}
		n[k{o.TenantID, o.Workload, o.Class, o.Status}]++
	}
	t.h.mu.Unlock()
	for x, c := range n {
		emit(float64(c), x.tenant, x.wl, x.class, x.status)
	}
}

func (t *telemetry) workloadCounts(emit func(float64, ...string)) {
	type k struct{ tenant, status string }
	n := map[k]int{}
	t.h.mu.Lock()
	wls := make([]*workload, 0, len(t.h.workloads))
	for _, w := range t.h.workloads {
		wls = append(wls, w)
	}
	t.h.mu.Unlock()
	for _, w := range wls {
		w.mu.Lock()
		n[k{w.tenantID, w.status}]++
		w.mu.Unlock()
	}
	for x, c := range n {
		emit(float64(c), x.tenant, x.status)
	}
}

func (t *telemetry) capacity(emit func(float64, ...string)) {
	c := t.h.capacity()
	emit(float64(c.Nodes), "nodes")
	emit(float64(c.VMs), "vms")
	emit(float64(c.VMMemMiB), "vm_memory_mib")
	emit(float64(c.HugeMemMiB), "huge_memory_mib")
	emit(float64(c.VCPUs), "vcpus")
	emit(float64(c.ScratchMiB), "scratch_mib")
}

func (t *telemetry) usage(emit func(float64, ...string)) {
	u := t.h.usage()
	emit(float64(u.Nodes), "nodes")
	emit(float64(u.VMs), "vms")
	emit(float64(u.VMMemMiB), "vm_memory_mib")
	emit(float64(u.HugeMemMiB), "huge_memory_mib")
	emit(u.VCPUs, "vcpus")
	emit(float64(u.ScratchMiB), "scratch_mib")
	emit(float64(u.Containers), "containers")
}

// wake counts a Durable Object instance starting in workerd (the
// platform's ensure with wake: a Host DO or plain-DO constructor).
func (t *telemetry) wake(o *object) {
	if o != nil && o.Class != "" {
		t.wakeups.Add(1, o.TenantID, o.Workload, o.Class)
	}
}

// ---- ingress ----

// ingressSpan is one request through hostd's ingress.
type ingressSpan struct {
	t      *telemetry
	w      *workload
	inst   *instance
	rec    *obs.Record
	parent string
	start  time.Time
	upg    bool
	rw     *obsWriter
}

// startIngress opens the request's SERVER span and sets the traceparent
// workerd (or the node app) receives. blob is netd's cf blob (the
// verified caller); auditTrace is the hop's platform trace ID.
func (t *telemetry) startIngress(w *workload, inst *instance, o *object, r *http.Request, blob map[string]any, auditTrace string, rw http.ResponseWriter) (*ingressSpan, http.ResponseWriter) {
	now := time.Now()
	rec := &obs.Record{Time: now, Kind: obs.KindRequest, Tenant: w.tenantID, Workload: w.name, Deployment: inst.depID,
		Object: o.ID, Class: o.Class, Name: o.Name, Method: r.Method, Path: r.URL.Path, AuditTraceID: auditTrace, Caller: callerOf(blob)}
	s := &ingressSpan{t: t, w: w, inst: inst, rec: rec, start: now, upg: r.Header.Get("Upgrade") != ""}
	// A tailnet caller's trace context is honoured; a public (Funnel)
	// request always starts a new trace.
	tc, ok := obs.ParseTraceparent(r.Header.Get(obs.HeaderTraceparent))
	if ok && !publicBlob(blob) {
		rec.TraceID, s.parent = tc.TraceID, tc.SpanID
	} else {
		tc.Flags = "01"
		rec.TraceID = auditTrace
		if !obs.ValidTraceID(rec.TraceID) {
			rec.TraceID = obs.NewTraceID()
		}
	}
	rec.SpanID, rec.ParentSpanID = obs.NewSpanID(), s.parent
	r.Header.Set(obs.HeaderTraceparent, obs.TraceContext{TraceID: rec.TraceID, SpanID: rec.SpanID, Flags: tc.Flags}.String())
	s.rw = &obsWriter{ResponseWriter: rw}
	return s, s.rw
}

// end finishes the span: metrics now, the record once joined.
func (s *ingressSpan) end() {
	t, rec := s.t, s.rec
	code := s.rw.code
	if code == 0 {
		code = 200
		if s.upg {
			code = 101 // hijacked: ReverseProxy wrote the 101 itself
		}
	}
	d := time.Since(s.start)
	rec.Status, rec.DurationMs = code, float64(d.Microseconds())/1000
	t.reqDur.Observe(d.Seconds(), rec.Method, strconv.Itoa(code), rec.Tenant, rec.Workload, rec.Class)
	name := rec.Method + " " + rec.Workload
	if rec.Class != "" {
		name = rec.Method + " " + rec.Class
	}
	st := obs.StatusUnset
	if code >= 500 {
		st = obs.StatusError
	}
	attrs := []obs.Attr{obs.A("http.request.method", rec.Method), obs.A("url.path", rec.Path), obs.A("http.response.status_code", int64(code)),
		obs.A(attrWorkload, rec.Workload), obs.A(attrObject, rec.Object), obs.A(attrClass, rec.Class), obs.A("tw.object.name", rec.Name),
		obs.A("tw.deployment.id", rec.Deployment), obs.A("tw.audit.trace_id", rec.AuditTraceID)}
	if c := rec.Caller; c != nil {
		attrs = append(attrs, obs.A("tw.caller.node", c.Node), obs.A("tw.caller.user", c.User), obs.A("tw.caller.object", c.Object))
		if c.Public {
			attrs = append(attrs, obs.A("tw.caller.public", true), obs.A("client.address", c.IP))
		}
	}
	t.span(s.w, obs.Span{TraceID: rec.TraceID, SpanID: rec.SpanID, ParentSpanID: s.parent, Name: name, Kind: obs.SpanKindServer,
		Start: s.start, End: s.start.Add(d), Attrs: attrs, Status: st})
	if s.inst.node != nil {
		t.emit(s.w, rec) // the node tier has no tail: nothing to join
		return
	}
	t.join(s.w, rec, true)
}

// callerOf reads the verified caller from netd's cf blob.
func callerOf(blob map[string]any) *obs.Caller {
	ts, _ := blob["tailscale"].(map[string]any)
	if rq, _ := ts["requester"].(map[string]any); rq["kind"] == "public" {
		ip, _ := rq["ip"].(string)
		return &obs.Caller{Public: true, IP: ip}
	}
	c, _ := ts["caller"].(map[string]any)
	if c == nil {
		return nil
	}
	out := &obs.Caller{}
	out.Node, _ = c["node"].(string)
	out.NodeID, _ = c["nodeId"].(string)
	out.User, _ = c["user"].(string)
	if tags, _ := c["tags"].([]any); len(tags) > 0 {
		for _, x := range tags {
			if s, ok := x.(string); ok {
				out.Tags = append(out.Tags, s)
			}
		}
	}
	if o, _ := c["tw"].(map[string]any); o != nil {
		out.Object, _ = o["objectId"].(string)
	}
	return out
}

// obsWriter records the status; Unwrap keeps Flush and Hijack reachable
// (WebSocket upgrades through ReverseProxy).
type obsWriter struct {
	http.ResponseWriter
	code int
}

func (o *obsWriter) WriteHeader(c int) {
	if o.code == 0 && c >= 200 || c == 101 {
		o.code = c
	}
	o.ResponseWriter.WriteHeader(c)
}

func (o *obsWriter) Write(b []byte) (int, error) {
	if o.code == 0 {
		o.code = 200
	}
	return o.ResponseWriter.Write(b)
}

func (o *obsWriter) Unwrap() http.ResponseWriter { return o.ResponseWriter }

func (o *obsWriter) Flush() {
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---- egress ----

type egressSpan struct {
	t      *telemetry
	inst   *instance
	o      *object
	r      *http.Request
	tc     obs.TraceContext
	parent string
	start  time.Time
}

// startEgress opens a CLIENT span for an egress request and passes it on
// as the destination's parent.
func (t *telemetry) startEgress(inst *instance, o *object, r *http.Request) *egressSpan {
	s := &egressSpan{t: t, inst: inst, o: o, r: r, start: time.Now()}
	if tc, ok := obs.ParseTraceparent(r.Header.Get(obs.HeaderTraceparent)); ok {
		s.tc, s.parent = tc, tc.SpanID
	} else {
		s.tc = obs.TraceContext{TraceID: obs.NewTraceID(), Flags: "01"}
	}
	s.tc.SpanID = obs.NewSpanID()
	r.Header.Set(obs.HeaderTraceparent, s.tc.String())
	return s
}

func (s *egressSpan) end(code int, errCode string) {
	d := time.Since(s.start)
	w, o, r := s.inst.w, s.o, s.r
	s.t.egressDur.Observe(d.Seconds(), r.Method, strconv.Itoa(code), errCode, w.tenantID, w.name, o.ID)
	host, port := r.URL.Hostname(), r.URL.Port()
	attrs := []obs.Attr{obs.A("http.request.method", r.Method), obs.A("server.address", host), obs.A("url.full", r.URL.Scheme+"://"+r.URL.Host+r.URL.Path),
		obs.A("http.response.status_code", int64(code)), obs.A("error.type", errCode), obs.A(attrWorkload, w.name), obs.A(attrObject, o.ID),
		obs.A(attrClass, o.Class), obs.A("tw.object.name", o.Name)}
	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, obs.A("server.port", int64(p)))
	}
	st := obs.StatusUnset
	if errCode != "" || code >= 500 {
		st = obs.StatusError
	}
	s.t.span(w, obs.Span{TraceID: s.tc.TraceID, SpanID: s.tc.SpanID, ParentSpanID: s.parent, Name: r.Method, Kind: obs.SpanKindClient,
		Start: s.start, End: s.start.Add(d), Attrs: attrs, Status: st, StatusMessage: errCode})
}

// ---- host API: tail events and runtime spans ----

type attested struct {
	Class string `json:"class"`
	Name  string `json:"name"`
}

type tailEvent struct {
	Time        float64 `json:"time"`
	Outcome     string  `json:"outcome"`
	Entrypoint  string  `json:"entrypoint"`
	Model       string  `json:"model"`
	DOID        string  `json:"doId"`
	Truncated   bool    `json:"truncated"`
	Event       string  `json:"event"`
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	Traceparent string  `json:"traceparent"`
	Status      int     `json:"status"`
	Route       *struct {
		Kind, Class, Name string
	} `json:"route"`
	Logs []struct {
		T     float64 `json:"t"`
		Level string  `json:"level"`
		Msg   string  `json:"msg"`
	} `json:"logs"`
	Exceptions []struct {
		T       float64 `json:"t"`
		Name    string  `json:"name"`
		Message string  `json:"message"`
		Stack   string  `json:"stack"`
	} `json:"exceptions"`
}

type runtimeSpan struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId"`
	Name         string         `json:"name"`
	Start        float64        `json:"start"`
	End          float64        `json:"end"`
	Error        bool           `json:"error"`
	Attrs        map[string]any `json:"attrs"`

	// Kind is "server" for an entry span (DO RPC, alarm); else CLIENT.
	Kind string `json:"kind"`
}

// maxTailBody bounds one POST /v1/tail (the platform slims events first).
const maxTailBody = 4 << 20

// hostAPI serves POST /v1/tail and POST /v1/spans from the platform worker
// of inst (only it reaches the host API).
func (t *telemetry) hostAPI(inst *instance, w http.ResponseWriter, r *http.Request) {
	var body struct {
		As     *attested       `json:"as"`
		Events []tailEvent     `json:"events"`
		Spans  []runtimeSpan   `json:"spans"`
		Raw    json.RawMessage `json:"-"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTailBody)).Decode(&body); err != nil {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "bad telemetry body"})
		return
	}
	if body.As != nil && inst.app.Classes[body.As.Class] == nil {
		body.As = nil // not a class of this app: attribute to the workload
	}
	switch r.URL.Path {
	case "/v1/tail":
		for i := range body.Events {
			t.tail(inst, body.As, &body.Events[i])
		}
	case "/v1/spans":
		t.runtimeSpans(inst, body.As, body.Spans)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func msTime(ms float64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

func (t *telemetry) tail(inst *instance, as *attested, ev *tailEvent) {
	w := inst.w
	rec := &obs.Record{Time: msTime(ev.Time), Kind: obs.KindInvocation, Tenant: w.tenantID, Workload: w.name, Deployment: inst.depID,
		Event: ev.Event, Entrypoint: ev.Entrypoint, Outcome: ev.Outcome, Truncated: ev.Truncated, Method: ev.Method, Path: ev.Path, Status: ev.Status}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	switch {
	case as != nil: // a facet: attested by the platform
		rec.Class, rec.Name = as.Class, as.Name
	case ev.Model == "durableObject":
		rec.Class, rec.DurableObjectID = ev.Entrypoint, ev.DOID
		if ev.Route != nil && ev.Route.Kind == "object" && ev.Route.Class == ev.Entrypoint {
			rec.Name = ev.Route.Name
		}
	}
	if rec.Class != "" && rec.Name != "" {
		t.h.mu.Lock()
		if o := t.h.byKey[objKey(w.tenantID, w.name, rec.Class, rec.Name)]; o != nil {
			rec.Object = o.ID
		}
		t.h.mu.Unlock()
	} else if rec.Class == "" {
		if o := w.currentWL(); o != nil {
			rec.Object = o.ID
		}
	}
	for _, l := range ev.Logs {
		rec.Logs = append(rec.Logs, obs.LogLine{Time: msTime(l.T), Level: l.Level, Message: l.Msg})
	}
	for _, x := range ev.Exceptions {
		rec.Exceptions = append(rec.Exceptions, obs.Exception{Time: msTime(x.T), Name: x.Name, Message: x.Message, Stack: inst.mapStack(x.Stack)}) // stackmap.go
	}
	t.invocations.Add(1, w.tenantID, w.name, rec.Class, ev.Event, ev.Outcome)
	if tc, ok := obs.ParseTraceparent(ev.Traceparent); ok {
		rec.TraceID, rec.SpanID = tc.TraceID, tc.SpanID
		if ev.Event == "scheduled" {
			t.emit(w, rec) // a cron run: hostd's root span, no ingress half
			return
		}
		t.join(w, rec, false)
		return
	}
	if k, ok := recordEntryKey(w, rec); ok {
		t.entryRecord(k, w, rec) // DO RPC, alarm: telemetry_entries.go
		return
	}
	t.emit(w, rec)
}

// runtimeSpans takes the runtime's DO-call spans: observability data from
// the tenant's isolate, so validated and bounded, attributed to the
// attested caller.
func (t *telemetry) runtimeSpans(inst *instance, as *attested, spans []runtimeSpan) {
	w := inst.w
	now := time.Now()
	if len(spans) > 64 {
		spans = spans[:64]
	}
	for _, rs := range spans {
		if !obs.ValidTraceID(rs.TraceID) || !obs.ValidSpanID(rs.SpanID) || (rs.ParentSpanID != "" && !obs.ValidSpanID(rs.ParentSpanID)) {
			continue
		}
		st, en := msTime(rs.Start), msTime(rs.End)
		if st.IsZero() || en.Before(st) || st.Before(now.Add(-time.Hour)) || en.After(now.Add(time.Minute)) {
			continue
		}
		attrs := []obs.Attr{obs.A(attrWorkload, w.name)}
		if as != nil {
			attrs = append(attrs, obs.A(attrClass, as.Class), obs.A("tw.object.name", as.Name))
		}
		for _, k := range []string{
			"tw.do.class",
			"tw.do.id",
			"tw.do.name",
			"tw.trigger",
			"http.request.method",
			"rpc.method",
		} {
			if v, ok := rs.Attrs[k].(string); ok && len(v) <= 256 {
				attrs = append(attrs, obs.A(k, v))
			}
		}
		if v, ok := rs.Attrs["http.response.status_code"].(float64); ok && v >= 0 && v < 1000 {
			attrs = append(attrs, obs.A("http.response.status_code", int64(v)))
		}
		name := rs.Name
		if len(name) > 128 {
			name = name[:128]
		}
		sc := obs.StatusUnset
		if rs.Error {
			sc = obs.StatusError
		}
		kind := obs.SpanKindClient
		if rs.Kind == "server" {
			kind = obs.SpanKindServer
		}
		if k, ok := spanEntryKey(w, as, &rs); ok {
			t.entrySpanArrived(k, entrySpan{traceID: rs.TraceID, spanID: rs.SpanID, parent: rs.ParentSpanID, start: st})
		}
		t.span(w, obs.Span{TraceID: rs.TraceID, SpanID: rs.SpanID, ParentSpanID: rs.ParentSpanID, Name: name, Kind: kind,
			Start: st, End: en, Attrs: attrs, Status: sc})
	}
}

// ---- join ----

// join pairs the ingress half (ingress=true) and the tail half of a
// request by span ID; the first to arrive waits up to JoinWait.
func (t *telemetry) join(w *workload, rec *obs.Record, ingress bool) {
	key := rec.SpanID
	t.mu.Lock()
	p, ok := t.pending[key]
	if ok && p.ingress != ingress {
		delete(t.pending, key)
		t.mu.Unlock()
		if ingress {
			t.emit(w, merge(rec, p.rec))
		} else {
			t.emit(w, merge(p.rec, rec))
		}
		return
	}
	var old *pendingHalf
	if ok {
		old = p // a second half of the same kind: write the first alone
	}
	if len(t.pending) >= maxPending {
		t.mu.Unlock()
		t.emit(w, rec)
		return
	}
	t.pending[key] = &pendingHalf{w: w, rec: rec, ingress: ingress, deadline: time.Now().Add(t.opt.JoinWait)}
	t.mu.Unlock()
	if old != nil {
		t.emit(old.w, old.rec)
	}
}

// merge completes the ingress record with the invocation's half.
func merge(in, tail *obs.Record) *obs.Record {
	in.Logs, in.Exceptions, in.Outcome, in.Event, in.Entrypoint = tail.Logs, tail.Exceptions, tail.Outcome, tail.Event, tail.Entrypoint
	in.Truncated = in.Truncated || tail.Truncated
	if in.Class == "" && tail.Class != "" { // /_tw/o/<Class>/<name> via the workload node
		in.Class, in.Name, in.DurableObjectID = tail.Class, tail.Name, tail.DurableObjectID
		if tail.Object != "" {
			in.Object = tail.Object
		}
	}
	return in
}

// flushPending writes every half whose wait is over (all, with zero now).
func (t *telemetry) flushPending(now time.Time) {
	var due []*pendingHalf
	t.mu.Lock()
	for k, p := range t.pending {
		if now.IsZero() || now.After(p.deadline) {
			due = append(due, p)
			delete(t.pending, k)
		}
	}
	t.mu.Unlock()
	for _, p := range due {
		t.emit(p.w, p.rec)
	}
	t.flushEntries(now)
}

// ---- output ----

// RequestLogPath is where tenantID's workload writes its request records.
func (h *Host) RequestLogPath(tenantID, workload string) string {
	return filepath.Join(h.tenantDir(tenantID), "logs", workload+".requests.jsonl")
}

// emit writes a finished record to the workload's request log (rate
// limited) and queues it for export.
func (t *telemetry) emit(w *workload, rec *obs.Record) {
	rec.Host = t.h.cfg.HostID
	rec.Bound()
	if e := t.exporter(w); e != nil {
		e.addLogs(rec.LogRecords())
	}
	l := t.recordLog(w)
	if l == nil {
		return
	}
	if !l.lim.Allow(time.Now()) {
		t.mu.Lock()
		l.dropped++
		t.mu.Unlock()
		t.logsDropped.Add(1, w.tenantID, w.name)
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.f.Write(append(b, '\n'))
}

func (t *telemetry) recordLog(w *workload) *recordLog {
	k := w.tenantID + "/" + w.name
	t.mu.Lock()
	defer t.mu.Unlock()
	if l := t.logs[k]; l != nil {
		return l
	}
	f, err := obs.OpenRotating(t.h.RequestLogPath(w.tenantID, w.name), t.opt.RecordFileMax)
	if err != nil {
		t.h.log.Warn("request log unavailable", "workload", w.name, "err", err)
		return nil
	}
	l := &recordLog{f: f, lim: obs.NewLimiter(t.opt.RecordRate, t.opt.RecordBurst)}
	t.logs[k] = l
	return l
}

// reportDrops writes one "dropped" record per workload and second.
func (t *telemetry) reportDrops(now time.Time) {
	type drop struct {
		l      *recordLog
		tenant string
		wl     string
		n      int
	}
	var ds []drop
	t.mu.Lock()
	for k, l := range t.logs {
		if l.dropped > 0 && now.Sub(l.lastReport) >= time.Second {
			tn, wl, _ := strings.Cut(k, "/")
			ds = append(ds, drop{l, tn, wl, l.dropped})
			l.dropped, l.lastReport = 0, now
		}
	}
	t.mu.Unlock()
	for _, d := range ds {
		b, _ := json.Marshal(&obs.Record{Time: now, Kind: obs.KindDropped, Host: t.h.cfg.HostID, Tenant: d.tenant, Workload: d.wl, Dropped: d.n})
		d.l.f.Write(append(b, '\n'))
	}
}

func (t *telemetry) span(w *workload, s obs.Span) {
	if e := t.exporter(w); e != nil {
		e.addSpan(s)
	}
}

func (t *telemetry) loop() {
	defer t.wg.Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	lastExport, lastRefresh := time.Now(), time.Now()
	for {
		select {
		case <-t.stop:
			return
		case now := <-tick.C:
			t.flushPending(now)
			t.reportDrops(now)
			if now.Sub(lastExport) >= t.opt.ExportEvery {
				lastExport = now
				t.exportAll(now)
			}
			if now.Sub(lastRefresh) >= t.opt.RefreshEvery {
				lastRefresh = now
				t.refreshAll()
			}
		}
	}
}

// ---- OTLP export ----

// exporter is one workload's OTLP export: its queues and its config,
// released by mgmt to this host as the workload's lease holder.
type exporter struct {
	t        *telemetry
	w        *workload
	mu       sync.Mutex
	known    bool                // the config has been read once
	cfg      *mgmtapi.OTLPExport // nil: export nothing
	client   *obs.OTLPClient
	logs     []obs.LogRecord
	spans    []obs.Span
	busy     bool
	lastMet  time.Time
	fetching bool
	lastTry  time.Time
}

// exporterOf returns w's exporter, created on first use.
func (t *telemetry) exporterOf(w *workload) *exporter {
	k := w.tenantID + "/" + w.name
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.exps[k]
	if e == nil {
		e = &exporter{t: t, w: w}
		t.exps[k] = e
	}
	return e
}

// exporter returns w's exporter while it may export (its config is
// fetched on first use; items queue meanwhile), or nil once mgmt said the
// tenant exports nothing.
func (t *telemetry) exporter(w *workload) *exporter {
	e := t.exporterOf(w)
	e.mu.Lock()
	known, cfg, retry := e.known, e.cfg, time.Since(e.lastTry) >= 5*time.Second
	e.mu.Unlock()
	if !known {
		if retry {
			e.refresh()
		}
		return e
	}
	if cfg == nil {
		return nil
	}
	return e
}

// workloadStarted reads w's export config now (a deploy).
func (t *telemetry) workloadStarted(w *workload) {
	t.exporterOf(w).refresh()
}

func (t *telemetry) refreshAll() {
	t.mu.Lock()
	es := make([]*exporter, 0, len(t.exps))
	for _, e := range t.exps {
		es = append(es, e)
	}
	t.mu.Unlock()
	for _, e := range es {
		e.refresh()
	}
}

// refresh reads the config from mgmt (in the background, one at a time).
func (e *exporter) refresh() {
	e.mu.Lock()
	if e.fetching {
		e.mu.Unlock()
		return
	}
	e.fetching, e.lastTry = true, time.Now()
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			e.fetching = false
			e.mu.Unlock()
		}()
		epoch := e.w.epochNow()
		if epoch <= 0 {
			return // not deployed here (yet)
		}
		ctx, cancel := context.WithTimeout(e.t.h.ctxOrBackground(), 15*time.Second)
		defer cancel()
		cfg, err := e.t.h.mgmt.TelemetryGrant(ctx, e.w.tenantID, e.w.name, epoch)
		if err != nil {
			if ae, ok := mgmtapi.AsError(err); ok && (ae.Code == "lease_lost" || ae.Status == 404 || ae.Status == 405) {
				cfg = nil // not ours any more (or a mgmt without OTLP export): export nothing
			} else {
				e.t.h.log.Debug("telemetry config unavailable; will retry", "workload", e.w.name, "err", err)
				return
			}
		}
		e.set(cfg)
	}()
}

func (e *exporter) set(cfg *mgmtapi.OTLPExport) {
	e.mu.Lock()
	defer e.mu.Unlock()
	first := !e.known
	e.known = true
	if cfg == nil {
		e.cfg, e.client, e.logs, e.spans = nil, nil, nil, nil
		return
	}
	if e.cfg == nil || e.cfg.Endpoint != cfg.Endpoint || first {
		e.t.h.log.Info("OTLP export configured", "tenant", e.w.tenantID, "workload", e.w.name, "endpoint", cfg.Endpoint, "signals", cfg.Signals)
	}
	e.cfg = cfg
	e.client = &obs.OTLPClient{Endpoint: cfg.Endpoint, Headers: cfg.Headers, HTTP: e.httpClient()}
	if !e.wants(obs.SignalLogs) {
		e.logs = nil
	}
	if !e.wants(obs.SignalTraces) {
		e.spans = nil
	}
}

func (e *exporter) wants(sig string) bool {
	if e.cfg == nil {
		return !e.known // queue until the config is known
	}
	for _, s := range e.cfg.Signals {
		if s == sig {
			return true
		}
	}
	return false
}

// httpClient dials the collector from the workload's own tailnet node
// (netd's egress dialer: tailnet peers only, ADR-019), so the tailnet
// policy decides whether the workload may reach it.
func (e *exporter) httpClient() *http.Client {
	w := e.w
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			wl := w.currentWL()
			w.mu.Lock()
			tn := w.tenant
			w.mu.Unlock()
			if wl == nil || tn == nil || tn.reg == nil {
				return nil, &apiError{Code: "identity_pending", Detail: "workload node not created yet"}
			}
			return tn.reg.DialEgress(ctx, wl.NetdID, addr)
		},
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

func (e *exporter) addLogs(rs []obs.LogRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.wants(obs.SignalLogs) {
		return
	}
	// Over the bound: drop the oldest (queued first, then new).
	if over := len(e.logs) + len(rs) - e.t.opt.QueueMax; over > 0 {
		if e.known {
			e.t.expDropped.Add(float64(over), e.w.tenantID, e.w.name, obs.SignalLogs)
		}
		if old := len(e.logs); over >= old {
			e.logs = e.logs[:0]
			rs = rs[min(len(rs), over-old):]
		} else {
			e.logs = e.logs[over:]
		}
	}
	e.logs = append(e.logs, rs...)
}

func (e *exporter) addSpan(s obs.Span) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.wants(obs.SignalTraces) {
		return
	}
	if len(e.spans) >= e.t.opt.QueueMax {
		if e.known {
			e.t.expDropped.Add(1, e.w.tenantID, e.w.name, obs.SignalTraces)
		}
		e.spans = e.spans[1:]
	}
	e.spans = append(e.spans, s)
}

func (t *telemetry) exportAll(now time.Time) {
	t.mu.Lock()
	es := make([]*exporter, 0, len(t.exps))
	for _, e := range t.exps {
		es = append(es, e)
	}
	t.mu.Unlock()
	for _, e := range es {
		e.export(now)
	}
}

// export sends the queues (and, every MetricsEvery, the metrics) in the
// background, one export at a time per workload.
func (e *exporter) export(now time.Time) {
	e.mu.Lock()
	if e.busy || e.cfg == nil || e.client == nil {
		e.mu.Unlock()
		return
	}
	logs, spans := e.logs, e.spans
	e.logs, e.spans = nil, nil
	metrics := e.wants(obs.SignalMetrics) && now.Sub(e.lastMet) >= e.t.opt.MetricsEvery
	if metrics {
		e.lastMet = now
	}
	if len(logs) == 0 && len(spans) == 0 && !metrics {
		e.mu.Unlock()
		return
	}
	e.busy = true
	c := e.client
	e.mu.Unlock()
	t, w := e.t, e.w
	res := t.resource(w)
	go func() {
		defer func() {
			e.mu.Lock()
			e.busy = false
			e.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(t.h.ctxOrBackground(), 30*time.Second)
		defer cancel()
		fail := func(sig string, n int, err error) {
			t.expErrors.Add(1, w.tenantID, w.name, sig)
			t.expDropped.Add(float64(n), w.tenantID, w.name, sig)
			t.h.log.Warn("OTLP export failed", "tenant", w.tenantID, "workload", w.name, "signal", sig, "items", n, "err", err)
		}
		for len(logs) > 0 {
			n := min(len(logs), 512)
			if err := c.Export(ctx, obs.SignalLogs, obs.LogsPayload(res, logs[:n])); err != nil {
				fail(obs.SignalLogs, len(logs), err)
				break
			}
			logs = logs[n:]
		}
		for len(spans) > 0 {
			n := min(len(spans), 512)
			if err := c.Export(ctx, obs.SignalTraces, obs.TracesPayload(res, spans[:n])); err != nil {
				fail(obs.SignalTraces, len(spans), err)
				break
			}
			spans = spans[n:]
		}
		if metrics {
			snap := t.reg.Snapshot(func(l map[string]string) bool { return l[attrTenant] == w.tenantID && l[attrWorkload] == w.name })
			if len(snap) > 0 {
				if err := c.Export(ctx, obs.SignalMetrics, obs.MetricsPayload(res, snap)); err != nil {
					fail(obs.SignalMetrics, len(snap), err)
				}
			}
		}
	}()
}

// resource describes a workload's telemetry (OTel resource attributes).
func (t *telemetry) resource(w *workload) []obs.Attr {
	return []obs.Attr{obs.A("service.name", w.name), obs.A("service.namespace", w.tenantID), obs.A("service.instance.id", t.h.cfg.HostID+"/"+w.name),
		obs.A("host.name", t.h.cfg.HostID), obs.A(attrTenant, w.tenantID), obs.A(attrWorkload, w.name),
		obs.A("telemetry.sdk.name", "tw"), obs.A("telemetry.sdk.language", "go")}
}

// ctxOrBackground is the host's context once started.
func (h *Host) ctxOrBackground() context.Context {
	if h.ctx != nil {
		return h.ctx
	}
	return context.Background()
}

// TelemetryExporting reports whether tenantID's workload has an OTLP
// export configured on this host (tests).
func (h *Host) TelemetryExporting(tenantID, workload string) bool {
	h.tel.mu.Lock()
	e := h.tel.exps[tenantID+"/"+workload]
	h.tel.mu.Unlock()
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg != nil
}
