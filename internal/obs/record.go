package obs

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/termsafe"
)

// Record kinds.
const (
	// KindRequest: a request that came through hostd's ingress (any tier),
	// joined with its workerd invocation when there is one.
	KindRequest = "request"
	// KindInvocation: a workerd invocation with no ingress half (a DO
	// called from the Worker, an alarm, a cron run, an RPC call).
	KindInvocation = "invocation"
	// KindDropped: records dropped by the workload's rate limit.
	KindDropped = "dropped"
)

// Record bounds (per record; ADR-045 "Safety").
const (
	MaxLogLines     = 100
	MaxLogLineBytes = 4 << 10
	MaxLogBytes     = 16 << 10
	MaxExceptions   = 8
	MaxStackBytes   = 4 << 10
)

// Record is one line of a workload's request log (JSON Lines) and the
// unit `tw logs` shows. Bodies, query strings and request headers are
// never in it.
type Record struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Host       string    `json:"host,omitempty"`
	Tenant     string    `json:"tenant,omitempty"`
	Workload   string    `json:"workload,omitempty"`
	Deployment string    `json:"deployment,omitempty"`
	// Object is the tailworks object ID (o-…); Class/Name the DO it is (empty
	// for the workload's own node). DurableObjectID is workerd's ID for an
	// internal invocation of a plain-DO class whose name the tail does not
	// carry.
	Object          string  `json:"object,omitempty"`
	Class           string  `json:"class,omitempty"`
	Name            string  `json:"name,omitempty"`
	DurableObjectID string  `json:"durableObjectId,omitempty"`
	Caller          *Caller `json:"caller,omitempty"`
	Method          string  `json:"method,omitempty"`
	Path            string  `json:"path,omitempty"`
	Status          int     `json:"status,omitempty"`
	DurationMs      float64 `json:"durationMs,omitempty"`
	// Event is the invocation's trigger (fetch, alarm, rpc, scheduled, …);
	// Entrypoint workerd's entrypoint (the DO class).
	Event        string      `json:"event,omitempty"`
	Entrypoint   string      `json:"entrypoint,omitempty"`
	Outcome      string      `json:"outcome,omitempty"`
	TraceID      string      `json:"traceId,omitempty"`
	SpanID       string      `json:"spanId,omitempty"`
	ParentSpanID string      `json:"parentSpanId,omitempty"`
	AuditTraceID string      `json:"auditTraceId,omitempty"`
	Logs         []LogLine   `json:"logs,omitempty"`
	Exceptions   []Exception `json:"exceptions,omitempty"`
	Truncated    bool        `json:"truncated,omitempty"`
	Dropped      int         `json:"dropped,omitempty"`
}

// Caller is the verified caller (from netd's WhoIs; never from tenant code).
type Caller struct {
	Node   string   `json:"node,omitempty"`
	NodeID string   `json:"nodeId,omitempty"`
	User   string   `json:"user,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Object string   `json:"object,omitempty"` // the calling tw object (node certificate)
	Public bool     `json:"public,omitempty"`
	IP     string   `json:"ip,omitempty"` // public requests: the client IP Funnel reports
}

// String is a short display form.
func (c *Caller) String() string {
	switch {
	case c == nil:
		return ""
	case c.Public:
		if c.IP != "" {
			return "public:" + c.IP
		}
		return "public"
	case c.User != "":
		return c.User
	case c.Node != "":
		h, _, _ := strings.Cut(c.Node, ".")
		return h
	}
	return c.NodeID
}

// LogLine is one console.* call.
type LogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Exception is one uncaught exception.
type Exception struct {
	Time    time.Time `json:"time,omitempty"`
	Name    string    `json:"name,omitempty"`
	Message string    `json:"message,omitempty"`
	Stack   string    `json:"stack,omitempty"`
}

// Bound applies the per-record limits in place.
func (r *Record) Bound() {
	total := 0
	keep := r.Logs[:0]
	for _, l := range r.Logs {
		if len(keep) >= MaxLogLines || total >= MaxLogBytes {
			r.Truncated = true
			break
		}
		if len(l.Message) > MaxLogLineBytes {
			l.Message = cut(l.Message, MaxLogLineBytes)
			r.Truncated = true
		}
		if total+len(l.Message) > MaxLogBytes {
			l.Message = cut(l.Message, MaxLogBytes-total)
			r.Truncated = true
		}
		total += len(l.Message)
		keep = append(keep, l)
	}
	r.Logs = keep
	if len(r.Exceptions) > MaxExceptions {
		r.Exceptions, r.Truncated = r.Exceptions[:MaxExceptions], true
	}
	for i := range r.Exceptions {
		e := &r.Exceptions[i]
		e.Name, e.Message = cut(e.Name, 256), cut(e.Message, MaxLogLineBytes)
		if len(e.Stack) > MaxStackBytes {
			e.Stack, r.Truncated = cut(e.Stack, MaxStackBytes), true
		}
	}
	r.Path = cut(r.Path, 2048)
}

// cut truncates s to at most n bytes on a UTF-8 boundary, marking it.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return ""
	}
	i := n - 3
	for i > 0 && s[i]&0xc0 == 0x80 {
		i--
	}
	return s[:i] + "…"
}

// IsError reports whether the record is an error: a 4xx/5xx status, a
// non-ok outcome or an exception.
func (r *Record) IsError() bool {
	return r.Status >= 400 || (r.Outcome != "" && r.Outcome != "ok") || len(r.Exceptions) > 0
}

// Filter selects records (`tw logs` flags; hostd applies it).
type Filter struct {
	Object  string // object ID, Class/name, Class or name
	Status  string // "404", "4xx", "5xx"
	Outcome string // ok, exception, exceededCpu, canceled, …
	Caller  string // substring of the caller's user, node or node ID
	Trace   string // trace ID (or prefix), or audit trace ID
	Errors  bool
}

// Empty reports whether f selects everything.
func (f Filter) Empty() bool { return f == Filter{} }

// Values encodes f as query parameters.
func (f Filter) Values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("object", f.Object)
	set("status", f.Status)
	set("outcome", f.Outcome)
	set("caller", f.Caller)
	set("trace", f.Trace)
	if f.Errors {
		v.Set("errors", "1")
	}
	return v
}

// ParseFilter reads f from query parameters.
func ParseFilter(q url.Values) (Filter, error) {
	f := Filter{Object: q.Get("object"), Status: q.Get("status"), Outcome: q.Get("outcome"), Caller: q.Get("caller"), Trace: strings.ToLower(q.Get("trace"))}
	f.Errors = q.Get("errors") == "1" || q.Get("errors") == "true"
	if f.Status != "" {
		if _, _, err := statusRange(f.Status); err != nil {
			return Filter{}, err
		}
	}
	for _, s := range []string{f.Object, f.Outcome, f.Caller, f.Trace} {
		if len(s) > 256 {
			return Filter{}, fmt.Errorf("filter value too long")
		}
	}
	return f, nil
}

func statusRange(s string) (lo, hi int, err error) {
	if len(s) == 3 && (s[1:] == "xx" || s[1:] == "XX") && s[0] >= '1' && s[0] <= '5' {
		d := int(s[0]-'0') * 100
		return d, d + 99, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 100 || n > 599 {
		return 0, 0, fmt.Errorf("status must be a code (404) or a class (5xx)")
	}
	return n, n, nil
}

// Match reports whether r passes f.
func (f Filter) Match(r *Record) bool {
	if f.Errors && !r.IsError() {
		return false
	}
	if f.Status != "" {
		lo, hi, err := statusRange(f.Status)
		if err != nil || r.Status < lo || r.Status > hi {
			return false
		}
	}
	if f.Outcome != "" && !strings.EqualFold(r.Outcome, f.Outcome) {
		return false
	}
	if f.Object != "" {
		cn := r.Class + "/" + r.Name
		if f.Object != r.Object && f.Object != cn && f.Object != r.Class && f.Object != r.Name && f.Object != r.DurableObjectID {
			return false
		}
	}
	if f.Caller != "" {
		c := r.Caller
		if c == nil {
			return false
		}
		s := strings.ToLower(f.Caller)
		if !strings.Contains(strings.ToLower(c.User), s) && !strings.Contains(strings.ToLower(c.Node), s) && c.NodeID != f.Caller && c.Object != f.Caller && !(c.Public && s == "public") {
			return false
		}
	}
	if f.Trace != "" && !strings.HasPrefix(r.TraceID, f.Trace) && r.AuditTraceID != f.Trace {
		return false
	}
	return true
}

// Summary is r's one-line description (no time, no trace).
func (r *Record) Summary() string {
	var b strings.Builder
	switch r.Kind {
	case KindDropped:
		return fmt.Sprintf("tw: %d log records dropped (rate limit for this workload)", r.Dropped)
	case KindInvocation:
		ev := r.Event
		if ev == "" {
			ev = "invocation"
		}
		b.WriteString(ev)
		if r.Method != "" {
			b.WriteString(" " + r.Method + " " + r.Path)
		}
		if r.Outcome != "" {
			b.WriteString(" " + r.Outcome)
		}
	default:
		fmt.Fprintf(&b, "%s %s %d %s", r.Method, r.Path, r.Status, fmtDur(r.DurationMs))
		if r.Outcome != "" && r.Outcome != "ok" {
			b.WriteString(" " + r.Outcome)
		}
	}
	switch {
	case r.Class != "" && r.Name != "":
		b.WriteString("  " + r.Class + "/" + r.Name)
	case r.Class != "":
		b.WriteString("  " + r.Class)
	}
	if c := r.Caller.String(); c != "" {
		b.WriteString("  caller=" + c)
	}
	return b.String()
}

// Text renders r for a terminal: one summary line, then its console lines
// and exceptions indented. Every field is escaped (internal/termsafe): the
// path, console output and exceptions are tenant- or internet-controlled.
func (r *Record) Text() string {
	var b strings.Builder
	b.WriteString(r.Time.UTC().Format("2006-01-02T15:04:05.000Z"))
	b.WriteByte(' ')
	b.WriteString(termsafe.String(r.Summary()))
	if r.Kind != KindDropped && r.TraceID != "" {
		b.WriteString("  trace=" + termsafe.String(r.TraceID))
	}
	b.WriteByte('\n')
	if r.Kind == KindDropped {
		return b.String()
	}
	for _, l := range r.Logs {
		lvl := l.Level
		if lvl == "" {
			lvl = "log"
		}
		for i, line := range strings.Split(strings.TrimRight(l.Message, "\n"), "\n") {
			if i == 0 {
				fmt.Fprintf(&b, "    %-5s %s\n", termsafe.String(lvl), termsafe.Line(line))
			} else {
				fmt.Fprintf(&b, "          %s\n", termsafe.Line(line))
			}
		}
	}
	for _, e := range r.Exceptions {
		fmt.Fprintf(&b, "    error %s: %s\n", termsafe.String(e.Name), termsafe.String(e.Message))
		for _, line := range strings.Split(strings.TrimRight(FoldRuntimeFrames(e.Stack), "\n"), "\n") { // stack.go
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(&b, "          %s\n", termsafe.Line(strings.TrimSpace(line)))
			}
		}
	}
	if r.Truncated {
		b.WriteString("    (truncated)\n")
	}
	return b.String()
}

func fmtDur(ms float64) string {
	switch {
	case ms >= 1000:
		return strconv.FormatFloat(ms/1000, 'f', 2, 64) + "s"
	case ms >= 10:
		return strconv.FormatFloat(ms, 'f', 0, 64) + "ms"
	}
	return strconv.FormatFloat(ms, 'f', 1, 64) + "ms"
}

// Attrs are r's OpenTelemetry attributes (shared by its log records).
func (r *Record) Attrs() []Attr {
	a := []Attr{
		A("tw.workload", r.Workload), A("tw.deployment.id", r.Deployment), A("tw.object.id", r.Object),
		A("tw.object.class", r.Class), A("tw.object.name", r.Name), A("tw.invocation.outcome", r.Outcome),
		A("tw.invocation.event", r.Event), A("tw.audit.trace_id", r.AuditTraceID),
	}
	if r.Method != "" {
		a = append(a, A("http.request.method", r.Method), A("url.path", r.Path))
	}
	if r.Status != 0 {
		a = append(a, A("http.response.status_code", int64(r.Status)))
	}
	if r.Caller != nil {
		a = append(a, A("tw.caller.node", r.Caller.Node), A("tw.caller.user", r.Caller.User), A("tw.caller.object", r.Caller.Object))
		if len(r.Caller.Tags) > 0 {
			a = append(a, A("tw.caller.tags", r.Caller.Tags))
		}
		if r.Caller.Public {
			a = append(a, A("tw.caller.public", true))
		}
	}
	return a
}

// LogRecords converts r for OTLP: one record for the request itself
// (event.name tw.request / tw.invocation), one per console line and one
// per exception, all with r's trace and span.
func (r *Record) LogRecords() []LogRecord {
	attrs := r.Attrs()
	var out []LogRecord
	sev, st := SevInfo, "INFO"
	if r.IsError() {
		sev, st = SevError, "ERROR"
	}
	body := r.Summary()
	ev := "tw." + r.Kind
	out = append(out, LogRecord{Time: r.Time, Severity: sev, SeverityText: st, Body: body,
		Attrs: append(attrs[:len(attrs):len(attrs)], A("event.name", ev), A("tw.duration_ms", r.DurationMs)), TraceID: r.TraceID, SpanID: r.SpanID})
	for _, l := range r.Logs {
		s, t := SeverityOf(l.Level)
		out = append(out, LogRecord{Time: l.Time, Severity: s, SeverityText: t, Body: l.Message,
			Attrs: append(attrs[:len(attrs):len(attrs)], A("log.source", "console")), TraceID: r.TraceID, SpanID: r.SpanID})
	}
	for _, e := range r.Exceptions {
		out = append(out, LogRecord{Time: e.Time, Severity: SevError, SeverityText: "ERROR", Body: e.Name + ": " + e.Message,
			Attrs:   append(attrs[:len(attrs):len(attrs)], A("exception.type", e.Name), A("exception.message", e.Message), A("exception.stacktrace", e.Stack)),
			TraceID: r.TraceID, SpanID: r.SpanID})
	}
	return out
}
