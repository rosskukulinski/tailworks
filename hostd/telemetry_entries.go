package hostd

// Entry spans (ADR-045, #295): a Durable Object RPC call or alarm has no
// request, so workerd's tail event for it carries no traceparent. The
// runtime opens the invocation's SERVER span itself (rpctrace.ts) and
// reports it on POST /v1/spans; the tail event arrives on its own. The two
// are matched here by workload, object and event (an alarm, or an RPC
// method), nearest start time first, so the invocation's record carries
// the trace. The first to arrive waits up to JoinWait for the other; a
// record that finds no span is written without a trace, as before.

import (
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// entryWindow is how far apart a span's start and its record's time may
// be (both are workerd's clock at the invocation's start).
const entryWindow = 2 * time.Second

// entryKey names one object's invocations of one kind.
type entryKey struct {
	tenant, workload, class, obj, event string
}

type entrySpan struct {
	traceID, spanID, parent string
	start, deadline         time.Time
}

type entryRec struct {
	w        *workload
	rec      *obs.Record
	deadline time.Time
}

type entryJoin struct {
	mu    sync.Mutex
	spans map[entryKey][]entrySpan
	recs  map[entryKey][]*entryRec
	n     int // entries in both maps (bounded by maxPending)
}

func (t *telemetry) entryTable() *entryJoin {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = &entryJoin{spans: map[entryKey][]entrySpan{}, recs: map[entryKey][]*entryRec{}}
	}
	return t.entries
}

// entryEvent is the join's event for an RPC method or an alarm.
func entryEvent(trigger, method string) string {
	switch trigger {
	case "alarm":
		return "alarm"
	case "rpc":
		if method != "" {
			return "rpc:" + method
		}
	}
	return ""
}

// recordEntryKey is the key of a tail record that may have an entry span:
// an alarm or an RPC call of an object, with no traceparent.
func recordEntryKey(w *workload, rec *obs.Record) (entryKey, bool) {
	ev := ""
	switch rec.Event {
	case "alarm":
		ev = "alarm"
	case "rpc":
		ev = entryEvent("rpc", rec.Method)
	}
	k := entryKey{tenant: w.tenantID, workload: w.name, class: rec.Class, event: ev}
	switch {
	case rec.DurableObjectID != "": // a plain DO: workerd's ID
		k.obj = "id:" + rec.DurableObjectID
	case rec.Name != "": // a facet: attested by the platform
		k.obj = "name:" + rec.Name
	}
	return k, ev != "" && k.class != "" && k.obj != ""
}

// spanEntryKey is the key of a runtime entry span: tw.do.id for a plain
// DO (the tail's durableObjectId), else the attested object's name.
func spanEntryKey(w *workload, as *attested, rs *runtimeSpan) (entryKey, bool) {
	str := func(k string) string {
		v, _ := rs.Attrs[k].(string)
		return v
	}
	k := entryKey{tenant: w.tenantID, workload: w.name, event: entryEvent(str("tw.trigger"), str("rpc.method"))}
	if id := str("tw.do.id"); id != "" {
		k.class, k.obj = str("tw.do.class"), "id:"+id
	} else if as != nil {
		k.class, k.obj = as.Class, "name:"+as.Name
	}
	return k, rs.Kind == "server" && k.event != "" && k.class != "" && k.obj != ""
}

// nearest returns the index of the time in ts closest to at, within
// entryWindow (-1 if none).
func nearest(n int, at time.Time, tm func(int) time.Time) int {
	best, bestD := -1, entryWindow+1
	for i := range n {
		d := tm(i).Sub(at)
		if d < 0 {
			d = -d
		}
		if d <= entryWindow && d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// entrySpanArrived takes a runtime entry span: it completes a waiting
// record, or waits for one.
func (t *telemetry) entrySpanArrived(k entryKey, s entrySpan) {
	j := t.entryTable()
	j.mu.Lock()
	rs := j.recs[k]
	if i := nearest(len(rs), s.start, func(i int) time.Time { return rs[i].rec.Time }); i >= 0 {
		r := rs[i]
		j.recs[k] = append(rs[:i:i], rs[i+1:]...)
		if len(j.recs[k]) == 0 {
			delete(j.recs, k)
		}
		j.n--
		j.mu.Unlock()
		setEntryTrace(r.rec, s)
		t.emit(r.w, r.rec)
		return
	}
	if j.n < maxPending {
		s.deadline = time.Now().Add(t.opt.JoinWait)
		j.spans[k] = append(j.spans[k], s)
		j.n++
	}
	j.mu.Unlock()
}

// entryRecord takes a tail record that may have an entry span: written
// with the span's trace once both are here, or alone after JoinWait.
func (t *telemetry) entryRecord(k entryKey, w *workload, rec *obs.Record) {
	j := t.entryTable()
	j.mu.Lock()
	ss := j.spans[k]
	if i := nearest(len(ss), rec.Time, func(i int) time.Time { return ss[i].start }); i >= 0 {
		s := ss[i]
		j.spans[k] = append(ss[:i:i], ss[i+1:]...)
		if len(j.spans[k]) == 0 {
			delete(j.spans, k)
		}
		j.n--
		j.mu.Unlock()
		setEntryTrace(rec, s)
		t.emit(w, rec)
		return
	}
	if j.n >= maxPending {
		j.mu.Unlock()
		t.emit(w, rec)
		return
	}
	j.recs[k] = append(j.recs[k], &entryRec{w: w, rec: rec, deadline: time.Now().Add(t.opt.JoinWait)})
	j.n++
	j.mu.Unlock()
}

func setEntryTrace(rec *obs.Record, s entrySpan) {
	rec.TraceID, rec.SpanID, rec.ParentSpanID = s.traceID, s.spanID, s.parent
}

// flushEntries writes every record whose wait is over and forgets spans
// nobody claimed (all of them, with zero now).
func (t *telemetry) flushEntries(now time.Time) {
	t.mu.Lock()
	j := t.entries
	t.mu.Unlock()
	if j == nil {
		return
	}
	var due []*entryRec
	j.mu.Lock()
	for k, rs := range j.recs {
		keep := rs[:0]
		for _, r := range rs {
			if now.IsZero() || now.After(r.deadline) {
				due = append(due, r)
				j.n--
			} else {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			delete(j.recs, k)
		} else {
			j.recs[k] = keep
		}
	}
	for k, ss := range j.spans {
		keep := ss[:0]
		for _, s := range ss {
			if now.IsZero() || now.After(s.deadline) {
				j.n--
			} else {
				keep = append(keep, s)
			}
		}
		if len(keep) == 0 {
			delete(j.spans, k)
		} else {
			j.spans[k] = keep
		}
	}
	j.mu.Unlock()
	for _, r := range due {
		t.emit(r.w, r.rec)
	}
}
