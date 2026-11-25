package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OTLP/HTTP with the JSON encoding (OTLP specification 1.x, "JSON Protobuf
// Encoding"): lowerCamelCase field names, trace and span IDs as hex,
// 64-bit integers as decimal strings, enums as numbers. Every OTLP/HTTP
// receiver (the OpenTelemetry Collector's otlphttp, and most vendors')
// accepts it at <endpoint>/v1/{logs,metrics,traces}.

// Signals.
const (
	SignalLogs    = "logs"
	SignalMetrics = "metrics"
	SignalTraces  = "traces"
)

// AllSignals in a stable order.
var AllSignals = []string{SignalLogs, SignalMetrics, SignalTraces}

// Scope is the instrumentation scope tailworks reports.
const Scope = "tw"

type kv struct {
	Key   string `json:"key"`
	Value anyV   `json:"value"`
}

type anyV struct {
	S   *string   `json:"stringValue,omitempty"`
	B   *bool     `json:"boolValue,omitempty"`
	I   *string   `json:"intValue,omitempty"`
	D   *float64  `json:"doubleValue,omitempty"`
	Arr *arrayVal `json:"arrayValue,omitempty"`
}

type arrayVal struct {
	Values []anyV `json:"values"`
}

func toAny(v any) anyV {
	switch x := v.(type) {
	case string:
		return anyV{S: &x}
	case bool:
		return anyV{B: &x}
	case int:
		s := strconv.Itoa(x)
		return anyV{I: &s}
	case int64:
		s := strconv.FormatInt(x, 10)
		return anyV{I: &s}
	case float64:
		return anyV{D: &x}
	case []string:
		a := &arrayVal{Values: make([]anyV, 0, len(x))}
		for _, e := range x {
			a.Values = append(a.Values, toAny(e))
		}
		return anyV{Arr: a}
	}
	s := fmt.Sprint(v)
	return anyV{S: &s}
}

func kvs(attrs []Attr) []kv {
	out := make([]kv, 0, len(attrs))
	for _, a := range attrs {
		if a.Value == nil {
			continue
		}
		if s, ok := a.Value.(string); ok && s == "" {
			continue
		}
		out = append(out, kv{a.Key, toAny(a.Value)})
	}
	return out
}

func nanos(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

type resource struct {
	Attributes []kv `json:"attributes"`
}

type scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ---- traces ----

type otlpSpan struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId,omitempty"`
	Name         string     `json:"name"`
	Kind         int        `json:"kind"`
	Start        string     `json:"startTimeUnixNano"`
	End          string     `json:"endTimeUnixNano"`
	Attributes   []kv       `json:"attributes,omitempty"`
	Status       *spanState `json:"status,omitempty"`
}

type spanState struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// TracesPayload is an ExportTraceServiceRequest.
func TracesPayload(res []Attr, spans []Span) any {
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		o := otlpSpan{TraceID: s.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID, Name: s.Name, Kind: s.Kind,
			Start: nanos(s.Start), End: nanos(s.End), Attributes: kvs(s.Attrs)}
		if s.Status != StatusUnset {
			o.Status = &spanState{Code: s.Status, Message: s.StatusMessage}
		}
		out = append(out, o)
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   resource{kvs(res)},
		"scopeSpans": []any{map[string]any{"scope": scope{Name: Scope}, "spans": out}},
	}}}
}

// ---- logs ----

// LogRecord is one OTLP log record.
type LogRecord struct {
	Time            time.Time
	Severity        int // OTLP SeverityNumber
	SeverityText    string
	Body            string
	Attrs           []Attr
	TraceID, SpanID string
}

// Severity numbers (OTLP).
const (
	SevDebug = 5
	SevInfo  = 9
	SevWarn  = 13
	SevError = 17
)

// SeverityOf maps a console level to an OTLP severity.
func SeverityOf(level string) (int, string) {
	switch level {
	case "debug", "trace":
		return SevDebug, "DEBUG"
	case "warn":
		return SevWarn, "WARN"
	case "error", "exception":
		return SevError, "ERROR"
	}
	return SevInfo, "INFO"
}

type otlpLog struct {
	Time         string `json:"timeUnixNano"`
	Observed     string `json:"observedTimeUnixNano"`
	Severity     int    `json:"severityNumber,omitempty"`
	SeverityText string `json:"severityText,omitempty"`
	Body         anyV   `json:"body"`
	Attributes   []kv   `json:"attributes,omitempty"`
	TraceID      string `json:"traceId,omitempty"`
	SpanID       string `json:"spanId,omitempty"`
}

// LogsPayload is an ExportLogsServiceRequest.
func LogsPayload(res []Attr, recs []LogRecord) any {
	now := nanos(time.Now())
	out := make([]otlpLog, 0, len(recs))
	for _, r := range recs {
		b := r.Body
		out = append(out, otlpLog{Time: nanos(r.Time), Observed: now, Severity: r.Severity, SeverityText: r.SeverityText,
			Body: anyV{S: &b}, Attributes: kvs(r.Attrs), TraceID: r.TraceID, SpanID: r.SpanID})
	}
	return map[string]any{"resourceLogs": []any{map[string]any{
		"resource":  resource{kvs(res)},
		"scopeLogs": []any{map[string]any{"scope": scope{Name: Scope}, "logRecords": out}},
	}}}
}

// ---- metrics ----

// MetricsPayload is an ExportMetricsServiceRequest (cumulative
// temporality) of a registry snapshot.
func MetricsPayload(res []Attr, ms []MetricData) any {
	now := nanos(time.Now())
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		var dps []map[string]any
		for _, p := range m.Points {
			dp := map[string]any{"attributes": kvs(p.Attrs), "startTimeUnixNano": nanos(p.Start), "timeUnixNano": now}
			switch m.Kind {
			case KindHistogram:
				bc := make([]string, len(p.Counts))
				for i, c := range p.Counts {
					bc[i] = strconv.FormatUint(c, 10)
				}
				dp["count"] = strconv.FormatUint(p.Count, 10)
				dp["sum"] = p.Sum
				dp["bucketCounts"] = bc
				dp["explicitBounds"] = m.Bounds
			default:
				dp["asDouble"] = p.Value
			}
			dps = append(dps, dp)
		}
		if len(dps) == 0 {
			continue
		}
		om := map[string]any{"name": m.Name, "unit": m.Unit, "description": m.Help}
		switch m.Kind {
		case KindCounter:
			om["sum"] = map[string]any{"aggregationTemporality": 2, "isMonotonic": true, "dataPoints": dps}
		case KindHistogram:
			om["histogram"] = map[string]any{"aggregationTemporality": 2, "dataPoints": dps}
		default:
			om["gauge"] = map[string]any{"dataPoints": dps}
		}
		out = append(out, om)
	}
	return map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     resource{kvs(res)},
		"scopeMetrics": []any{map[string]any{"scope": scope{Name: Scope}, "metrics": out}},
	}}}
}

// ---- client ----

// OTLPClient posts payloads to an OTLP/HTTP endpoint.
type OTLPClient struct {
	// Endpoint is the base URL (http(s)://host[:port][/path]); signals go
	// to <Endpoint>/v1/<signal>.
	Endpoint string
	// Headers are sent with every export (collector credentials). Never
	// logged.
	Headers map[string]string
	HTTP    *http.Client
}

// MaxPayload bounds one export body.
const MaxPayload = 4 << 20

// Export posts payload to signal's path. A non-2xx answer is an error
// (the body's first 256 bytes, never the headers).
func (c *OTLPClient) Export(ctx context.Context, signal string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(b) > MaxPayload {
		return fmt.Errorf("otlp: %s payload is %d bytes (max %d)", signal, len(b), MaxPayload)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(c.Endpoint, "/")+"/v1/"+signal, bytes.NewReader(b))
	if err != nil {
		return err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("otlp: %s export: %s: %s", signal, resp.Status, strings.TrimSpace(string(msg)))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// ValidateEndpoint checks an OTLP base URL: http or https, a host, no
// user info, query or fragment.
func ValidateEndpoint(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("endpoint must be an http:// or https:// URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return errors.New("endpoint needs a host")
	}
	if u.User != nil {
		return errors.New("endpoint must not carry user info; use a header")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("endpoint must not have a query or fragment")
	}
	return nil
}
