package obs

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSeriesLimit bounds the series of one metric. Further label sets
// fold into one overflow series (attribute otel.metric.overflow=true), as
// the OpenTelemetry SDK does, so per-object labels can't grow memory
// without bound.
const DefaultSeriesLimit = 2000

// OverflowAttr marks the overflow series.
const OverflowAttr = "otel.metric.overflow"

// DurationBuckets are the semantic conventions' default boundaries for
// http.server.request.duration (seconds).
var DurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// Metric kinds.
const (
	KindCounter   = "counter"
	KindHistogram = "histogram"
	KindGauge     = "gauge"
)

// Registry holds metrics. It is safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	metrics []*metric
	byName  map[string]*metric
	limit   int
	start   time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]*metric{}, limit: DefaultSeriesLimit, start: time.Now()}
}

// SetSeriesLimit changes the per-metric series bound (tests).
func (r *Registry) SetSeriesLimit(n int) {
	r.mu.Lock()
	r.limit = n
	r.mu.Unlock()
}

type metric struct {
	name, unit, help, kind string
	keys                   []string
	bounds                 []float64
	series                 map[string]*series
	gauge                  func(emit func(v float64, labels ...string))
}

type series struct {
	labels []string // nil = overflow
	start  time.Time
	value  float64
	counts []uint64 // histogram: len(bounds)+1
	sum    float64
	count  uint64
}

func (r *Registry) add(m *metric) *metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byName[m.name]; ok {
		return old
	}
	m.series = map[string]*series{}
	r.metrics = append(r.metrics, m)
	r.byName[m.name] = m
	return m
}

// Counter is a monotonic sum.
type Counter struct {
	r *Registry
	m *metric
}

// Counter registers (or returns) a counter. name and keys are OpenTelemetry
// names (dotted); the Prometheus exposition translates them.
func (r *Registry) Counter(name, unit, help string, keys ...string) *Counter {
	return &Counter{r, r.add(&metric{name: name, unit: unit, help: help, kind: KindCounter, keys: keys})}
}

// Add adds v (≥ 0) to the series of labels (one value per key).
func (c *Counter) Add(v float64, labels ...string) {
	if v < 0 || math.IsNaN(v) {
		return
	}
	c.r.mu.Lock()
	s := c.r.seriesLocked(c.m, labels)
	s.value += v
	c.r.mu.Unlock()
}

// Histogram is an explicit-bucket histogram.
type Histogram struct {
	r *Registry
	m *metric
}

// Histogram registers (or returns) a histogram with bounds.
func (r *Registry) Histogram(name, unit, help string, bounds []float64, keys ...string) *Histogram {
	return &Histogram{r, r.add(&metric{name: name, unit: unit, help: help, kind: KindHistogram, keys: keys, bounds: bounds})}
}

// Observe records v in the series of labels.
func (h *Histogram) Observe(v float64, labels ...string) {
	if math.IsNaN(v) {
		return
	}
	h.r.mu.Lock()
	s := h.r.seriesLocked(h.m, labels)
	i := sort.SearchFloat64s(h.m.bounds, v) // first bound ≥ v: buckets are (prev, bound]
	s.counts[i]++
	s.sum += v
	s.count++
	h.r.mu.Unlock()
}

// GaugeFunc registers a gauge read at collection time: f calls emit once
// per series.
func (r *Registry) GaugeFunc(name, unit, help string, keys []string, f func(emit func(v float64, labels ...string))) {
	r.add(&metric{name: name, unit: unit, help: help, kind: KindGauge, keys: keys, gauge: f})
}

// seriesLocked returns the series for labels (r.mu held).
func (r *Registry) seriesLocked(m *metric, labels []string) *series {
	labels = fitLabels(labels, len(m.keys))
	k := strings.Join(labels, "\x00")
	if s, ok := m.series[k]; ok {
		return s
	}
	if len(m.series) >= r.limit {
		k, labels = "\x01overflow", nil
		if s, ok := m.series[k]; ok {
			return s
		}
	}
	s := &series{labels: labels, start: time.Now()}
	if m.kind == KindHistogram {
		s.counts = make([]uint64, len(m.bounds)+1)
	}
	m.series[k] = s
	return s
}

func fitLabels(l []string, n int) []string {
	out := make([]string, n)
	copy(out, l)
	return out
}

// DeleteSeries drops every series of every metric whose labels match
// (an undeployed workload's).
func (r *Registry) DeleteSeries(match func(labels map[string]string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.metrics {
		for k, s := range m.series {
			if s.labels != nil && match(labelMap(m.keys, s.labels)) {
				delete(m.series, k)
			}
		}
	}
}

func labelMap(keys, vals []string) map[string]string {
	out := make(map[string]string, len(keys))
	for i, k := range keys {
		out[k] = vals[i]
	}
	return out
}

// Point is one series of a snapshot.
type Point struct {
	Attrs  []Attr // nil labels = overflow: {otel.metric.overflow: true}
	Start  time.Time
	Value  float64
	Counts []uint64
	Sum    float64
	Count  uint64
}

// MetricData is one metric of a snapshot.
type MetricData struct {
	Name, Unit, Help, Kind string
	Bounds                 []float64
	Points                 []Point
}

// Snapshot returns every metric's series whose labels pass keep (nil:
// all). Gauges are read now. Series with an empty label key are reported
// without that attribute.
func (r *Registry) Snapshot(keep func(labels map[string]string) bool) []MetricData {
	r.mu.Lock()
	ms := append([]*metric(nil), r.metrics...)
	r.mu.Unlock()
	var out []MetricData
	for _, m := range ms {
		md := MetricData{Name: m.name, Unit: m.unit, Help: m.help, Kind: m.kind, Bounds: m.bounds}
		if m.kind == KindGauge {
			now := time.Now()
			m.gauge(func(v float64, labels ...string) {
				labels = fitLabels(labels, len(m.keys))
				if keep != nil && !keep(labelMap(m.keys, labels)) {
					return
				}
				md.Points = append(md.Points, Point{Attrs: attrsOf(m.keys, labels), Start: now, Value: v})
			})
		} else {
			r.mu.Lock()
			keys := make([]string, 0, len(m.series))
			for k := range m.series {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				s := m.series[k]
				if s.labels == nil {
					if keep != nil {
						continue // overflow belongs to no one tenant
					}
					md.Points = append(md.Points, Point{Attrs: []Attr{A(OverflowAttr, true)}, Start: s.start, Value: s.value,
						Counts: append([]uint64(nil), s.counts...), Sum: s.sum, Count: s.count})
					continue
				}
				if keep != nil && !keep(labelMap(m.keys, s.labels)) {
					continue
				}
				md.Points = append(md.Points, Point{Attrs: attrsOf(m.keys, s.labels), Start: s.start, Value: s.value,
					Counts: append([]uint64(nil), s.counts...), Sum: s.sum, Count: s.count})
			}
			r.mu.Unlock()
		}
		if len(md.Points) > 0 || keep == nil {
			out = append(out, md)
		}
	}
	return out
}

func attrsOf(keys, vals []string) []Attr {
	out := make([]Attr, 0, len(keys))
	for i, k := range keys {
		if vals[i] == "" {
			continue
		}
		if k == "http.response.status_code" {
			if n, err := strconv.Atoi(vals[i]); err == nil {
				out = append(out, A(k, int64(n)))
				continue
			}
		}
		out = append(out, A(k, vals[i]))
	}
	return out
}

// PromName is the Prometheus name of an OpenTelemetry metric (the
// specification's translation): dots and other invalid characters become
// "_", the unit becomes a suffix (s → seconds, By → bytes; "1" and
// annotations like "{request}" add none) and counters end in _total.
func PromName(name, unit, kind string) string {
	n := sanitize(name)
	if u := promUnit(unit); u != "" && !strings.HasSuffix(n, "_"+u) {
		n += "_" + u
	}
	if kind == KindCounter && !strings.HasSuffix(n, "_total") {
		n += "_total"
	}
	return n
}

func promUnit(u string) string {
	switch u {
	case "s":
		return "seconds"
	case "ms":
		return "milliseconds"
	case "By":
		return "bytes"
	case "MiBy":
		return "mebibytes"
	case "", "1":
		return ""
	}
	if strings.HasPrefix(u, "{") {
		return ""
	}
	return sanitize(u)
}

func sanitize(s string) string {
	var b strings.Builder
	for i, c := range s {
		ok := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')
		if ok {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// WritePrometheus writes every metric in the Prometheus text exposition
// format (version 0.0.4).
func (r *Registry) WritePrometheus(w io.Writer) error {
	bw := bufio.NewWriter(w)
	for _, md := range r.Snapshot(nil) {
		name := PromName(md.Name, md.Unit, md.Kind)
		typ := map[string]string{KindCounter: "counter", KindHistogram: "histogram", KindGauge: "gauge"}[md.Kind]
		help := md.Help
		if help == "" {
			help = md.Name
		}
		fmt.Fprintf(bw, "# HELP %s %s\n# TYPE %s %s\n", name, escHelp(help), name, typ)
		for _, p := range md.Points {
			lbl := promLabels(p.Attrs)
			switch md.Kind {
			case KindHistogram:
				var cum uint64
				for i, b := range md.Bounds {
					cum += p.Counts[i]
					fmt.Fprintf(bw, "%s_bucket%s %d\n", name, promLabels(append(p.Attrs[:len(p.Attrs):len(p.Attrs)], A("le", fmtFloat(b)))), cum)
				}
				fmt.Fprintf(bw, "%s_bucket%s %d\n", name, promLabels(append(p.Attrs[:len(p.Attrs):len(p.Attrs)], A("le", "+Inf"))), p.Count)
				fmt.Fprintf(bw, "%s_sum%s %s\n%s_count%s %d\n", name, lbl, fmtFloat(p.Sum), name, lbl, p.Count)
			default:
				fmt.Fprintf(bw, "%s%s %s\n", name, lbl, fmtFloat(p.Value))
			}
		}
	}
	return bw.Flush()
}

func promLabels(attrs []Attr) string {
	if len(attrs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, a := range attrs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(sanitize(a.Key))
		b.WriteString(`="`)
		b.WriteString(escLabel(fmt.Sprint(a.Value)))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func escHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func fmtFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// Gatherers writes several registries (and writer funcs) as one exposition.
type Gatherers []interface{ WritePrometheus(io.Writer) error }

// WritePrometheus writes each in turn.
func (g Gatherers) WritePrometheus(w io.Writer) error {
	for _, x := range g {
		if x == nil {
			continue
		}
		if err := x.WritePrometheus(w); err != nil {
			return err
		}
	}
	return nil
}
