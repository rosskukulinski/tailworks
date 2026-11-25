//go:build e2edataplane

package dataplane

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The checkers (LOOP.md "Invariants"). Each returns a result with pass/fail
// and evidence; report.json holds them all.

type result struct {
	ID       string
	Pass     bool
	Summary  string
	Evidence []string `json:",omitempty"`
	Notes    []string `json:",omitempty"`
}

func (r *result) fail(format string, a ...any) {
	r.Pass = false
	if len(r.Evidence) < 25 {
		r.Evidence = append(r.Evidence, fmt.Sprintf(format, a...))
	}
}

func (r *result) note(format string, a ...any) {
	if len(r.Notes) < 25 {
		r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
	}
}

const maxRetries = 3 // testdata/dp/wrangler.jsonc

// inFault reports whether t is inside a fault window widened by slack.
func inFault(faults []faultEv, t time.Time, slack time.Duration) bool {
	for _, f := range faults {
		if !t.Before(f.Start.Add(-time.Second)) && !t.After(f.End.Add(slack)) {
			return true
		}
	}
	return false
}

// faultAt names the most recently started fault whose window (plus 15 s)
// contains t, or "".
func faultAt(faults []faultEv, t time.Time) string {
	kind, start := "", time.Time{}
	for _, f := range faults {
		if !t.Before(f.Start) && !t.After(f.End.Add(15*time.Second)) && f.Start.After(start) {
			kind, start = f.Kind, f.Start
		}
	}
	return kind
}

func hostOf(node string) string {
	if i := strings.Index(node, "-"); i > 0 {
		return node[:i]
	}
	return node
}

// checkD3 — Queues: every acknowledged send is delivered at least once;
// msg.attempts counts deliveries (no two deliveries with the same count,
// none counted that never reached queue() outside a fault); retried
// messages come back; poison messages stop at max_retries+1 deliveries and
// reach the dead-letter queue.
func checkD3(h *history) result {
	r := result{ID: "D3", Pass: true}
	byK := map[string][]delivery{}
	dlq := map[string]int{}
	for _, d := range h.dels {
		if d.Q == "jobs" {
			byK[d.K] = append(byK[d.K], d)
		} else {
			dlq[d.K]++
		}
	}
	acked, lost, gaps, dupAttempts, redeliv := 0, 0, 0, 0, 0
	for _, s := range h.sends {
		if !s.OK {
			continue
		}
		acked++
		ds := byK[s.K]
		if len(ds) == 0 {
			lost++
			r.fail("lost: %s acknowledged at %s, never delivered", s.K, s.Recv.Format("15:04:05.000"))
			continue
		}
		seen := map[int]int{}
		maxA := 0
		for _, d := range ds {
			seen[d.Attempts]++
			if d.Attempts > maxA {
				maxA = d.Attempts
			}
		}
		for a, c := range seen {
			if c > 1 {
				dupAttempts++
				r.fail("attempts: %s delivered %d times with attempts=%d (%s)", s.K, c, a, describe(ds))
			}
		}
		if len(seen) < maxA {
			// An attempt was counted for a delivery the consumer never
			// reported. Inside a fault window that is a crash mid-batch
			// (documented); outside, it is a counted hand-back (#387, #412).
			gaps++
			last := ds[len(ds)-1].At
			if inFault(h.faults, last, 10*time.Second) {
				r.note("attempt gap in a fault window: %s %s", s.K, describe(ds))
			} else {
				r.fail("attempt gap outside any fault: %s %s", s.K, describe(ds))
			}
		}
		redeliv += len(ds) - 1
		switch {
		case strings.HasSuffix(s.K, "-x"):
			if maxA > maxRetries+1 {
				r.fail("poison %s delivered with attempts %d > max_retries+1 (%s)", s.K, maxA, describe(ds))
			}
			if dlq[s.K] == 0 {
				r.fail("poison %s never reached the dead-letter queue (%s)", s.K, describe(ds))
			}
		case strings.HasSuffix(s.K, "-r"):
			if maxA < 2 {
				r.fail("retried %s delivered only once (%s)", s.K, describe(ds))
			}
		}
	}
	r.Summary = fmt.Sprintf("%d acked sends, %d lost, %d with duplicate attempt counts, %d with attempt gaps, %d redeliveries, %d dead-lettered",
		acked, lost, dupAttempts, gaps, redeliv, len(dlq))
	return r
}

func describe(ds []delivery) string {
	var b []string
	for _, d := range ds {
		b = append(b, fmt.Sprintf("%d@%s/%s", d.Attempts, hostOf(d.Node), d.At.Format("15:04:05.000")))
	}
	return strings.Join(b, " ")
}

// checkD1 — one live serving copy: once a copy at a newer epoch has
// answered, no older epoch answers a request sent after that, and no older
// epoch's side effect (a Pulse egress call) arrives more than pulseSlack
// after the newer copy's first. A live move (--replace --force-takeover)
// is allowed moveSlack: the old host stops at its next heartbeat
// (docs/locks.md "Failure modes").
func checkD1(h *history) result {
	r := result{ID: "D1", Pass: true}
	const pulseSlack = time.Second
	const moveSlack = 5 * time.Second
	allow := func(at time.Time) time.Duration {
		if faultAt(h.faults, at) == "move" {
			return moveSlack
		}
		return 0
	}
	firstAns := map[int64]time.Time{} // epoch -> first answer received
	for _, o := range h.regs {
		if !o.OK || o.Epoch == 0 {
			continue
		}
		if f, ok := firstAns[o.Epoch]; !ok || o.Recv.Before(f) {
			firstAns[o.Epoch] = o.Recv
		}
	}
	stale := 0
	for _, o := range h.regs {
		if !o.OK || o.Epoch == 0 {
			continue
		}
		for e, f := range firstAns {
			if e > o.Epoch && o.Sent.After(f.Add(allow(f))) {
				stale++
				r.fail("stale copy answered: %s %s on %s at epoch %d (sent %s), after epoch %d answered at %s",
					o.Op, "/reg", o.Host, o.Epoch, o.Sent.Format("15:04:05.000"), e, f.Format("15:04:05.000"))
				break
			}
		}
	}
	firstPulse := map[int64]time.Time{}
	for _, p := range h.pulses {
		if p.Epoch == 0 {
			continue
		}
		if f, ok := firstPulse[p.Epoch]; !ok || p.At.Before(f) {
			firstPulse[p.Epoch] = p.At
		}
	}
	late := 0
	for _, p := range h.pulses {
		for e, f := range firstPulse {
			if e > p.Epoch && p.At.After(f.Add(pulseSlack+allow(f))) {
				late++
				r.fail("stale side effect: pulse from %s at epoch %d at %s, %.1fs after epoch %d's first",
					p.Node, p.Epoch, p.At.Format("15:04:05.000"), p.At.Sub(f).Seconds(), e)
				break
			}
		}
	}
	r.Summary = fmt.Sprintf("%d epochs answered, %d stale answers, %d stale side effects (of %d pulses)", len(firstAns), stale, late, len(h.pulses))
	return r
}

// checkD2 — fencing: no two lock holders overlap (a holder's lease runs from
// its grant until its release, or its last successful renew as sent + TTL);
// tokens only go up across grants; a lost holder's late write is refused
// once a newer token has written.
func checkD2(h *history) result {
	r := result{ID: "D2", Pass: true}
	type iv struct {
		client     string
		token      int64
		epoch      int64
		start, end time.Time
	}
	var ivs []iv
	open := map[string]*iv{}
	closeIv := func(c string) {
		if o := open[c]; o != nil {
			ivs = append(ivs, *o)
			delete(open, c)
		}
	}
	evs := append([]lockEv(nil), h.locks...)
	sort.Slice(evs, func(i, j int) bool { return evs[i].Sent.Before(evs[j].Sent) })
	var lastGrant int64
	grants := 0
	for _, e := range evs {
		switch e.Kind {
		case "grant":
			grants++
			closeIv(e.Client)
			if e.Token <= lastGrant {
				r.fail("token went down: %s granted %d (epoch %d) after %d", e.Client, e.Token, e.Epoch, lastGrant)
			}
			lastGrant = e.Token
			open[e.Client] = &iv{e.Client, e.Token, e.Epoch, e.Recv, e.Sent.Add(lockTTL)}
		case "renew":
			if o := open[e.Client]; o != nil && e.Status == 200 && e.Token == o.token {
				o.end = e.Sent.Add(lockTTL)
			}
		case "release":
			if o := open[e.Client]; o != nil && e.Status == 200 && e.Sent.Before(o.end) {
				o.end = e.Sent
			}
			closeIv(e.Client)
		case "lost":
			closeIv(e.Client)
		}
	}
	for c := range open {
		closeIv(c)
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })
	overlaps := 0
	for i := range ivs {
		for j := i + 1; j < len(ivs); j++ {
			a, b := ivs[i], ivs[j]
			if a.client == b.client || !b.start.Before(a.end) {
				continue
			}
			// Across epochs, a move forgets every holder: documented for a
			// planned move or a takeover of a live host (docs/locks.md
			// "Failure modes"), but a takeover of a host that is down or
			// cut off must come after the old lease ran out.
			kind := faultAt(h.faults, b.start)
			msg := fmt.Sprintf("two holders: %s (token %d, epoch %d) until %s, %s (token %d, epoch %d) granted %s [fault %s]",
				a.client, a.token, a.epoch, a.end.Format("15:04:05.000"), b.client, b.token, b.epoch, b.start.Format("15:04:05.000"), kind)
			if a.epoch == b.epoch && forwardStepBefore(h.faults, b.start) { // skew_test.go
				r.note("known (#569, a forward clock step on the lock's host ends a lease early): %s", msg)
			} else if a.epoch == b.epoch || strings.HasSuffix(kind, "-takeover") {
				overlaps++
				r.fail("%s", msg)
			} else {
				r.note("documented (a move forgets holders): %s", msg)
			}
		}
	}
	// Late writes.
	var maxAccepted int64
	staleOK := 0
	for _, e := range evs {
		if e.Kind != "write" && e.Kind != "stale-write" {
			continue
		}
		if e.Status == 200 && e.Token < maxAccepted {
			staleOK++
			r.fail("store accepted token %d after %d", e.Token, maxAccepted)
		}
		if e.Status == 200 && e.Token > maxAccepted {
			maxAccepted = e.Token
		}
	}
	r.Summary = fmt.Sprintf("%d grants, %d holder intervals, %d overlaps, %d stale writes accepted", grants, len(ivs), overlaps, staleOK)
	return r
}

// checkReg — the DO register, per epoch: increments are unique and, in
// real-time order, increasing; a read sees every increment acknowledged
// before it was sent. An acknowledged write is durable across a restart on
// the same host (same epoch). Across a move the new copy starts empty
// (documented), so epochs are checked separately.
func checkReg(h *history) result {
	r := result{ID: "D1-reg", Pass: true}
	by := map[int64][]regOp{}
	for _, o := range h.regs {
		if o.OK && o.Op != "probe" {
			by[o.Epoch] = append(by[o.Epoch], o)
		}
	}
	bad := 0
	for e, ops := range by {
		vals := map[int]int{}
		for _, o := range ops {
			if o.Op == "incr" {
				vals[o.V]++
			}
		}
		for v, c := range vals {
			if c > 1 {
				bad++
				r.fail("epoch %d: %d increments returned %d", e, c, v)
			}
		}
		for _, a := range ops {
			if a.Op != "incr" {
				continue
			}
			for _, b := range ops {
				if a.Recv.Before(b.Sent) && b.V < a.V {
					bad++
					r.fail("epoch %d: %s at %s returned %d after incr→%d acknowledged at %s (lost write?)",
						e, b.Op, b.Sent.Format("15:04:05.000"), b.V, a.V, a.Recv.Format("15:04:05.000"))
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("%d epochs, %d violations", len(by), bad)
	return r
}

// checkD4 — every acknowledged D1 insert is in the database at the end.
func checkD4(h *history, final map[string]bool, finalErr error) result {
	r := result{ID: "D4", Pass: true}
	if finalErr != nil {
		r.fail("final read failed: %v", finalErr)
		return r
	}
	acked, missing := 0, 0
	for _, o := range h.d1s {
		if !o.OK {
			continue
		}
		acked++
		if !final[o.ID] {
			missing++
			r.fail("acknowledged insert %s (at %s) missing", o.ID, o.Recv.Format("15:04:05.000"))
		}
	}
	r.Summary = fmt.Sprintf("%d acknowledged inserts, %d missing, %d rows at the end", acked, missing, len(final))
	return r
}

// checkD6 — availability, reported (not gated) except well outside fault
// windows, where at least 90% of client ops must succeed.
func checkD6(h *history) result {
	r := result{ID: "D6", Pass: true}
	const slack = 60 * time.Second // a takeover's gap plus recovery
	var in, inOK, out, outOK int
	add := func(t time.Time, ok bool) {
		if inFault(h.faults, t, slack) {
			in++
			if ok {
				inOK++
			}
		} else {
			out++
			if ok {
				outOK++
			}
		}
	}
	for _, o := range h.sends {
		add(o.Sent, o.OK)
	}
	for _, o := range h.regs {
		if o.Op != "probe" {
			add(o.Sent, o.OK)
		}
	}
	for _, o := range h.d1s {
		add(o.Sent, o.OK)
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 100
		}
		return 100 * float64(a) / float64(b)
	}
	r.Summary = fmt.Sprintf("in fault windows: %.1f%% of %d ops ok; outside: %.1f%% of %d", pct(inOK, in), in, pct(outOK, out), out)
	if out >= 50 && pct(outOK, out) < 90 {
		r.fail("only %.1f%% of %d ops outside fault windows succeeded", pct(outOK, out), out)
	}
	return r
}
