package hostd

// A cron run's root span (ADR-045, #295). hostd dispatches every run, so
// it opens the trace: the run's audit trace ID is its trace ID (as for an
// ingress request with no traceparent), the platform hands the span on to
// scheduled() as traceparent, and the run's record, its egress and its DO
// calls join that trace.

import (
	"errors"
	"net/http"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

type cronSpan struct {
	t     *telemetry
	w     *workload
	inst  *instance
	cron  string
	audit string
	tc    obs.TraceContext
	start time.Time
}

// startCron sets req's traceparent to a new root span for the run.
func (t *telemetry) startCron(w *workload, inst *instance, cronExpr, auditTrace string, req *http.Request) *cronSpan {
	s := &cronSpan{t: t, w: w, inst: inst, cron: cronExpr, audit: auditTrace, start: time.Now()}
	s.tc = obs.TraceContext{TraceID: auditTrace, SpanID: obs.NewSpanID(), Flags: "01"}
	if !obs.ValidTraceID(s.tc.TraceID) {
		s.tc.TraceID = obs.NewTraceID()
	}
	req.Header.Set(obs.HeaderTraceparent, s.tc.String())
	return s
}

// end exports the span with the run's outcome.
func (s *cronSpan) end(res CronResult, err error) {
	if s.t == nil {
		return // a Host without telemetry (tests)
	}
	outcome := res.Outcome
	if err != nil {
		outcome = "exception"
		if errors.Is(err, errCronTimeout) {
			outcome = "exceededTimeout"
		}
	}
	st, msg := obs.StatusUnset, ""
	if outcome != "ok" {
		st, msg = obs.StatusError, outcome
	}
	attrs := []obs.Attr{obs.A(attrWorkload, s.w.name), obs.A("tw.cron", s.cron), obs.A("tw.invocation.outcome", outcome),
		obs.A("tw.deployment.id", s.inst.depID), obs.A("tw.audit.trace_id", s.audit)}
	s.t.span(s.w, obs.Span{TraceID: s.tc.TraceID, SpanID: s.tc.SpanID, Name: "scheduled " + s.w.name, Kind: obs.SpanKindServer,
		Start: s.start, End: time.Now(), Attrs: attrs, Status: st, StatusMessage: msg})
}
