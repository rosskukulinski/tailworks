package hostd

import (
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// DO RPC and alarm records, whose tail events carry no traceparent, get
// the trace of the runtime's entry span for the same object and event,
// whichever arrives first; nearest start wins; unmatched records are still
// written after JoinWait.
func TestEntrySpansJoinRecords(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{JoinWait: 200 * time.Millisecond})
	inst := w.active
	tr, sp1, sp2, parent := obs.NewTraceID(), obs.NewSpanID(), obs.NewSpanID(), obs.NewSpanID()
	now := time.Now()
	ms := func(t time.Time) float64 { return float64(t.UnixMilli()) }
	span := func(id, par string, start time.Time, attrs map[string]any) runtimeSpan {
		return runtimeSpan{TraceID: tr, SpanID: id, ParentSpanID: par, Name: "x", Start: ms(start), End: ms(start.Add(10 * time.Millisecond)), Kind: "server", Attrs: attrs}
	}
	// A plain DO's RPC: span first, matched by tw.do.id.
	h.tel.runtimeSpans(inst, nil, []runtimeSpan{span(sp1, parent, now, map[string]any{"tw.trigger": "rpc", "rpc.method": "ping", "tw.do.class": "Plain", "tw.do.id": "abc"})})
	h.tel.tail(inst, nil, &tailEvent{Time: ms(now), Outcome: "ok", Entrypoint: "Plain", Model: "durableObject", DOID: "abc", Event: "rpc", Method: "ping"})
	// A facet's alarm: record first, matched by the attested name; the
	// other alarm span (3 s later, outside entryWindow) is not taken.
	as := &attested{Class: "Obj", Name: "carol"}
	h.tel.tail(inst, as, &tailEvent{Time: ms(now), Outcome: "ok", Entrypoint: "Obj", Event: "alarm", Method: "__tw_alarm"})
	h.tel.runtimeSpans(inst, as, []runtimeSpan{
		span(obs.NewSpanID(), "", now.Add(3*time.Second), map[string]any{"tw.trigger": "alarm"}),
		span(sp2, "", now, map[string]any{"tw.trigger": "alarm"}),
	})
	// An RPC with no span at all.
	h.tel.tail(inst, nil, &tailEvent{Time: ms(now), Outcome: "ok", Entrypoint: "Plain", Model: "durableObject", DOID: "zzz", Event: "rpc", Method: "other"})
	h.tel.flushPending(time.Time{})

	recs := records(t, h.RequestLogPath("tnA", "web"))
	got := map[string]obs.Record{}
	for _, r := range recs {
		got[r.Event+"/"+r.Method] = r
	}
	if r := got["rpc/ping"]; r.TraceID != tr || r.SpanID != sp1 || r.ParentSpanID != parent {
		t.Fatalf("plain RPC record: %+v", r)
	}
	if r := got["alarm/__tw_alarm"]; r.TraceID != tr || r.SpanID != sp2 || r.ParentSpanID != "" || r.Name != "carol" {
		t.Fatalf("facet alarm record: %+v", r)
	}
	if r, ok := got["rpc/other"]; !ok || r.TraceID != "" {
		t.Fatalf("unmatched record: %+v (written: %v)", r, ok)
	}
	if n := h.tel.entries.n; n != 0 {
		t.Fatalf("%d entries left after the flush", n)
	}
}
