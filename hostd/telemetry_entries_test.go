package hostd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// exportedSpans is every span the collector has, by span ID.
func (c *collector) exportedSpans() map[string]map[string]any {
	spans := map[string]map[string]any{}
	for _, p := range c.find("/v1/traces") {
		rss, _ := p.body["resourceSpans"].([]any)
		for _, rs := range rss {
			sss, _ := rs.(map[string]any)["scopeSpans"].([]any)
			for _, ss := range sss {
				sps, _ := ss.(map[string]any)["spans"].([]any)
				for _, sp := range sps {
					m := sp.(map[string]any)
					spans[m["spanId"].(string)] = m
				}
			}
		}
	}
	return spans
}

// waitSpan waits until span id is exported, then checks its trace, parent,
// kind and (if given) name.
func waitSpan(t *testing.T, coll *collector, id, trace, parent string, kind float64, name string) map[string]any {
	t.Helper()
	var sp map[string]any
	waitFor(t, "span "+id+" at the collector", 15*time.Second, func() bool {
		sp = coll.exportedSpans()[id]
		return sp != nil
	})
	pid, _ := sp["parentSpanId"].(string)
	if sp["traceId"] != trace || pid != parent || sp["kind"] != kind || (name != "" && sp["name"] != name) {
		t.Fatalf("span %s: %v (want trace %s parent %q kind %v name %q)", id, sp, trace, parent, kind, name)
	}
	return sp
}

// waitRecord waits for the first invocation record that match accepts.
func waitRecord(t *testing.T, logPath, what string, match func(r obs.Record) bool) obs.Record {
	t.Helper()
	var out obs.Record
	waitFor(t, what, 15*time.Second, func() bool {
		for _, r := range readRecords(t, logPath) {
			if r.Kind == obs.KindInvocation && match(r) {
				out = r
				return true
			}
		}
		return false
	})
	return out
}

// testEntryTraces (#295): DO RPC gets a CLIENT/SERVER span pair with the
// caller's trace carried across, an alarm and a cron run each get a root
// span, and their records, egress and spans share that trace.
func testEntryTraces(t *testing.T, s *suite, coll *collector, logPath, base, in, t0 string) {
	for _, c := range []struct{ kind, cls, name string }{{"obj", "Obj", "carol"}, {"plain", "Plain", "dave"}} {
		t.Run(c.cls+"RPC", func(t *testing.T) {
			code, r := s.getJSON(base+"/rpc/"+c.kind+"/"+c.name, "traceparent", in)
			if code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			// The runtime's marker never reaches tenant code.
			if a := fmt.Sprint(get(r, "rpc", "args")); a != "[a 2]" {
				t.Fatalf("callee got args %s", a)
			}
			w, ok1 := obs.ParseTraceparent(fmt.Sprint(r["worker"]))
			ec, ok2 := obs.ParseTraceparent(fmt.Sprint(get(r, "rpc", "echoTraceparent")))
			if !ok1 || !ok2 || ec.TraceID != t0 {
				t.Fatalf("RPC egress not in the caller's trace: %v", r)
			}
			rec := waitRecord(t, logPath, c.cls+"'s RPC record", func(r obs.Record) bool {
				return r.Event == "rpc" && r.Method == "ping" && r.Class == c.cls && hasLog(r, "rpc "+c.cls) && r.TraceID != ""
			})
			if rec.TraceID != t0 || rec.ParentSpanID == "" || rec.Outcome != "ok" {
				t.Fatalf("RPC record: %+v", rec)
			}
			// CLIENT "<Class> ping" under the Worker's ingress span →
			// SERVER "<Class> ping" (the record's span) → egress.
			waitSpan(t, coll, rec.ParentSpanID, t0, w.SpanID, obs.SpanKindClient, c.cls+" ping")
			waitSpan(t, coll, rec.SpanID, t0, rec.ParentSpanID, obs.SpanKindServer, c.cls+" ping")
			waitSpan(t, coll, ec.SpanID, t0, rec.SpanID, obs.SpanKindClient, "GET")
		})
		t.Run(c.cls+"Alarm", func(t *testing.T) {
			if code, r := s.getJSON(base + "/arm/" + c.kind + "/" + c.name); code != 200 {
				t.Fatalf("arm: %d %v", code, r)
			}
			// Wait for the alarm's record before asking the object what the
			// alarm saw. workerd's tail can put the alarm's console.log on
			// a request to the object that runs at the same time, so
			// polling the object here left the alarm's record without its
			// log line under load (#410).
			rec := waitRecord(t, logPath, c.cls+"'s alarm record", func(r obs.Record) bool {
				return r.Event == "alarm" && r.Class == c.cls && hasLog(r, "alarm "+c.cls) && r.TraceID != ""
			})
			var seen string
			waitFor(t, c.cls+"'s alarm result", 10*time.Second, func() bool {
				_, r := s.getJSON(base + "/alarm/" + c.kind + "/" + c.name)
				seen, _ = r["alarm"].(string)
				return seen != ""
			})
			ec, ok := obs.ParseTraceparent(seen)
			if !ok {
				t.Fatalf("the alarm's egress carried no traceparent: %q", seen)
			}
			if rec.TraceID != ec.TraceID || rec.ParentSpanID != "" || rec.TraceID == t0 {
				t.Fatalf("alarm record: %+v (egress %s)", rec, seen)
			}
			waitSpan(t, coll, rec.SpanID, rec.TraceID, "", obs.SpanKindServer, c.cls+" alarm")
			waitSpan(t, coll, ec.SpanID, rec.TraceID, rec.SpanID, obs.SpanKindClient, "GET")
		})
	}
	t.Run("CronRun", func(t *testing.T) {
		res, err := s.e.Host.RunCron(context.Background(), s.e.TenantID, "obsprobe", "0 0 1 1 *", time.Now())
		if err != nil || res.Outcome != "ok" {
			t.Fatalf("cron run: %+v %v", res, err)
		}
		rec := waitRecord(t, logPath, "the cron run's record", func(r obs.Record) bool {
			return r.Event == "scheduled" && hasLog(r, "cron 0 0 1 1 *")
		})
		if !obs.ValidTraceID(rec.TraceID) || !obs.ValidSpanID(rec.SpanID) || rec.Outcome != "ok" {
			t.Fatalf("cron record: %+v", rec)
		}
		waitSpan(t, coll, rec.SpanID, rec.TraceID, "", obs.SpanKindServer, "scheduled obsprobe")
		// The run's egress is a child of the root span.
		waitFor(t, "the cron run's egress span", 15*time.Second, func() bool {
			for _, sp := range coll.exportedSpans() {
				if sp["traceId"] == rec.TraceID && sp["parentSpanId"] == rec.SpanID && sp["kind"] == float64(obs.SpanKindClient) {
					return true
				}
			}
			return false
		})
	})
}
