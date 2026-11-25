package mgmt

import (
	"io"
	"net/http"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// mgmt's own metrics (ADR-045), served on GET /metrics on mgmt's
// tailnet node (internal/mgmtd). One registry per process: replicas are
// separate processes, each scraped on its own node.
var metrics = newMgmtMetrics()

type mgmtMetrics struct {
	reg   *obs.Registry
	mint  *obs.Histogram
	stage *obs.Histogram
}

// MintBuckets cover a node mint, from a pooled key (~0.1 s) to a cold
// registration behind the throttle (tens of seconds).
var MintBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

func newMgmtMetrics() *mgmtMetrics {
	r := obs.NewRegistry()
	return &mgmtMetrics{
		reg: r,
		mint: r.Histogram("tw.mint.duration", "s", "Object node mints: create to minted (the \"mint spans\" log line's create_to_minted_ms).",
			MintBuckets, "tw.tenant.id", "tw.mint.pooled"),
		stage: r.Histogram("tw.mint.stage.duration", "s", "One stage of a mint attempt (queued, load, claim, throttle, reclaim, broker, createkey, custody, commit).",
			MintBuckets, "tw.mint.stage"),
	}
}

// observe records a finished mint attempt.
func (s *mintSpans) observe(tenantID string, pooled bool) {
	p := "false"
	if pooled {
		p = "true"
	}
	sec := func(d time.Duration) float64 { return d.Seconds() }
	metrics.mint.Observe(sec(s.queued+s.last.Sub(s.start)), tenantID, p)
	for _, st := range []struct {
		name string
		d    time.Duration
	}{{"queued", s.queued}, {"load", s.load}, {"claim", s.claim}, {"throttle", s.throttle}, {"reclaim", s.reclaim},
		{"broker", s.broker}, {"createkey", s.createKey}, {"custody", s.custody}, {"commit", s.commit}} {
		if st.d > 0 {
			metrics.stage.Observe(sec(st.d), st.name)
		}
	}
}

// Metrics is this process's mgmt registry.
func Metrics() *obs.Registry { return metrics.reg }

// MetricsHandler serves GET /metrics: mgmt's registry, then extra's (an
// embedded host's, single-process tw up; read at each scrape).
func MetricsHandler(extra func() obs.Gatherers) http.Handler {
	return obs.MetricsHandler(gatherFunc(func(w io.Writer) error {
		g := obs.Gatherers{metrics.reg}
		if extra != nil {
			g = append(g, extra()...)
		}
		return g.WritePrometheus(w)
	}))
}

type gatherFunc func(io.Writer) error

func (f gatherFunc) WritePrometheus(w io.Writer) error { return f(w) }
