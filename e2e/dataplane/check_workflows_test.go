//go:build e2edataplane

package dataplane

// The Workflows checkers, W1–W5 (#418, ledger/workflows.md, LOOP.md).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// wfIndex groups the history by instance.
type wfIndex struct {
	dos     map[string][]wfDo // inst/step -> calls, in arrival order
	notes   map[string][]wfNote
	events  map[string][]wfEvent
	decs    map[string][]wfDecision
	created map[string]wfCreate
	finals  map[string]wfFinal
	insts   []string
}

func indexWF(wh *wfHist) *wfIndex {
	x := &wfIndex{dos: map[string][]wfDo{}, notes: map[string][]wfNote{}, events: map[string][]wfEvent{},
		decs: map[string][]wfDecision{}, created: map[string]wfCreate{}, finals: map[string]wfFinal{}}
	seen := map[string]bool{}
	add := func(i string) {
		if !seen[i] {
			seen[i] = true
			x.insts = append(x.insts, i)
		}
	}
	for _, c := range wh.creates {
		x.created[c.ID] = c
		add(c.ID)
	}
	ds := append([]wfDo(nil), wh.dos...)
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
	for _, d := range ds {
		x.dos[d.Inst+"/"+d.Step] = append(x.dos[d.Inst+"/"+d.Step], d)
		add(d.Inst)
	}
	ns := append([]wfNote(nil), wh.notes...)
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].At.Before(ns[j].At) })
	for _, n := range ns {
		x.notes[n.Inst] = append(x.notes[n.Inst], n)
		add(n.Inst)
	}
	for _, e := range wh.events {
		x.events[e.Inst] = append(x.events[e.Inst], e)
	}
	for _, d := range wh.decs {
		x.decs[d.Inst] = append(x.decs[d.Inst], d)
	}
	for _, f := range wh.finals {
		x.finals[f.Inst] = f
	}
	sort.Strings(x.insts)
	return x
}

func (x *wfIndex) firstNote(inst, kind string) (wfNote, bool) {
	for _, n := range x.notes[inst] {
		if n.Kind == kind {
			return n, true
		}
	}
	return wfNote{}, false
}

// activity is everything an instance did, as (time, host), in time order.
func (x *wfIndex) activity(inst string) []wfAct {
	var out []wfAct
	for k, ds := range x.dos {
		if strings.HasPrefix(k, inst+"/") {
			for _, d := range ds {
				out = append(out, wfAct{d.At, hostOf(d.Node), "do " + d.Step})
			}
		}
	}
	for _, n := range x.notes[inst] {
		out = append(out, wfAct{n.At, hostOf(n.Node), "note " + n.Kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

type wfAct struct {
	at   time.Time
	host string
	what string
}

// storageHost is where an instance's Durable Object lives: the host of its
// first activity, else the host its create was sent to.
func (x *wfIndex) storageHost(inst string) string {
	if a := x.activity(inst); len(a) > 0 {
		return a[0].host
	}
	if c, ok := x.created[inst]; ok && c.OK {
		return c.Host
	}
	return ""
}

var wfSteps = []string{"s0", "s1", "s2", "pre-sleep", "final"}

// checkW1 — steps: each completed step's stored result is returned on every
// replay and never recomputed; side-effect calls per step are ≥ 1 and none
// arrives after run() has seen the step complete; a planned failure retries
// from the failed step; a complete instance's output holds the same results.
func checkW1(wh *wfHist) result {
	r := result{ID: "W1", Pass: true}
	x := indexWF(wh)
	steps, replays, retried, bad := 0, 0, 0, 0
	for _, inst := range x.insts {
		runs := 0
		for _, n := range x.notes[inst] {
			if n.Kind == "run" {
				runs++
			}
		}
		if runs > 1 {
			replays += runs - 1
		}
		seen := map[string]wfNote{}
		for _, st := range wfSteps {
			calls := x.dos[inst+"/"+st]
			var first *wfNote
			vals := map[string]bool{}
			for _, n := range x.notes[inst] {
				if n.Kind == "seen" && n.Step == st {
					if first == nil {
						n := n
						first = &n
					}
					vals[n.Call] = true
				}
			}
			if first == nil {
				continue
			}
			steps++
			seen[st] = *first
			if len(vals) > 1 {
				bad++
				r.fail("%s %s: run() saw %d different results across replays (recomputed): %v", inst, st, len(vals), keys(vals))
			}
			found := false
			for _, c := range calls {
				if c.Call == first.Call {
					found = true
				}
				if c.At.After(first.At) {
					bad++
					r.fail("%s %s: side effect call %s (#%d) at %s, after run() saw the step complete at %s",
						inst, st, c.Call[:8], c.N, c.At.Format("15:04:05.000"), first.At.Format("15:04:05.000"))
				}
			}
			if !found {
				bad++
				r.fail("%s %s: the stored result's call %s never reached the recorder (%d calls)", inst, st, first.Call, len(calls))
			}
			if len(calls) > 1 && st != "s1" {
				r.note("%s %s: %d calls before it completed (at least once)", inst, st, len(calls))
			}
		}
		// The planned failure: s1 fails its first call, and retries from itself.
		if instNum(inst)%3 == 0 {
			if s1, ok := seen["s1"]; ok {
				retried++
				if n := len(x.dos[inst+"/s1"]); n < 2 {
					bad++
					r.fail("%s: s1 completed (%s) after %d call(s); its first call fails", inst, s1.At.Format("15:04:05.000"), n)
				}
				if s0, ok := seen["s0"]; ok && len(x.dos[inst+"/s0"]) > 0 && x.dos[inst+"/s1"][0].At.Before(s0.At) {
					bad++
					r.fail("%s: s1 ran before s0 completed", inst)
				}
			}
		}
		// A complete instance returns the results run() saw.
		if f, ok := x.finals[inst]; ok && f.Code == 200 && f.Status == "complete" {
			var out struct {
				Out   map[string]struct{ Call string } `json:"out"`
				Final struct{ Call string }            `json:"final"`
			}
			if err := json.Unmarshal(f.Output, &out); err != nil {
				bad++
				r.fail("%s: complete, output unreadable: %v (%s)", inst, err, f.Output)
				continue
			}
			for st, sn := range seen {
				got := out.Out[st].Call
				if st == "final" {
					got = out.Final.Call
				}
				if st == "pre-sleep" {
					continue
				}
				if got != sn.Call {
					bad++
					r.fail("%s: output's %s is call %q, run() saw %q", inst, st, got, sn.Call)
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("%d instances, %d completed steps, %d replays of run(), %d planned step failures retried, %d violations", len(x.insts), steps, replays, retried, bad)
	return r
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// faultOverlap is how long [from, to] overlaps fault windows (each widened
// by 30 s for recovery).
func faultOverlap(faults []faultEv, from, to time.Time) time.Duration {
	var sum time.Duration
	for _, f := range faults {
		s, e := f.Start, f.End.Add(30*time.Second)
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			sum += e.Sub(s)
		}
	}
	return sum
}

// leaseHost is the host the lease was on at t, from the recorded deployment
// changes ("" before the first).
func leaseHost(deps []depEv, t time.Time) string {
	h := ""
	for _, d := range deps {
		if d.At.After(t) {
			break
		}
		h = d.Host
	}
	return h
}

// arrives reports whether the watch saw the lease come to host in (from, to].
func arrives(deps []depEv, host string, from, to time.Time) bool {
	for _, d := range deps {
		if d.Host == host && d.At.After(from) && !d.At.After(to) {
			return true
		}
	}
	return false
}

// leftHost reports whether the lease was off host at any time in [from, to].
func leftHost(deps []depEv, host string, from, to time.Time) bool {
	if leaseHost(deps, from) != host {
		return true
	}
	for _, d := range deps {
		if d.At.After(from) && d.At.Before(to) && d.Host != host {
			return true
		}
	}
	return false
}

// checkW2 — waits: a sleep finishes no earlier than its deadline, and no
// later than the deadline plus the time faults kept the instance down (+10
// s); a wait for an event or an approval finishes no earlier than the first
// send, and no later than the first accepted send plus that time.
func checkW2(wh *wfHist, faults []faultEv, deps []depEv) result {
	r := result{ID: "W2", Pass: true}
	x := indexWF(wh)
	const slack = 10 * time.Second
	sleeps, waits, bad, late := 0, 0, 0, 0
	var worst time.Duration
	upper := func(inst, what string, from, at time.Time) {
		over := at.Sub(from)
		if over > worst {
			worst = over
		}
		host := x.storageHost(inst)
		if leftHost(deps, host, from, at) {
			r.note("%s %s: %.1fs late, but the lease left %s meanwhile", inst, what, over.Seconds(), host)
			return
		}
		if allowed := slack + faultOverlap(faults, from, at); over > allowed {
			late++
			r.fail("%s %s: finished %.1fs after it could (allowed %.1fs: faults overlapped %.1fs)",
				inst, what, over.Seconds(), allowed.Seconds(), (allowed - slack).Seconds())
		}
	}
	for _, inst := range x.insts {
		if w, ok := x.firstNote(inst, "woke"); ok {
			sleeps++
			dl := time.UnixMilli(w.Deadline)
			if w.At.Before(dl) {
				bad++
				r.fail("%s: sleep ended at %s, %.3fs before its deadline %s", inst, w.At.Format("15:04:05.000"), dl.Sub(w.At).Seconds(), dl.Format("15:04:05.000"))
			}
			upper(inst, "sleep", dl, w.At)
		}
		if ev, ok := x.firstNote(inst, "event"); ok {
			waits++
			if ev.Ev != "ev-"+inst {
				bad++
				r.fail("%s: the wait returned event %q", inst, ev.Ev)
			}
			var firstSent, firstOK time.Time
			for _, e := range x.events[inst] {
				if firstSent.IsZero() || e.Sent.Before(firstSent) {
					firstSent = e.Sent
				}
				if e.Status == 200 && (firstOK.IsZero() || e.Recv.Before(firstOK)) {
					firstOK = e.Recv
				}
			}
			switch {
			case firstSent.IsZero():
				bad++
				r.fail("%s: the event wait ended at %s, and no event was ever sent", inst, ev.At.Format("15:04:05.000"))
			case ev.At.Before(firstSent):
				bad++
				r.fail("%s: the event wait ended at %s, before the first send at %s", inst, ev.At.Format("15:04:05.000"), firstSent.Format("15:04:05.000"))
			case !firstOK.IsZero():
				upper(inst, "event wait", firstOK, ev.At)
			}
		}
		if dn, ok := x.firstNote(inst, "decided"); ok && dn.Error == "" {
			waits++
			var firstSent, firstOK time.Time
			for _, d := range x.decs[inst] {
				if firstSent.IsZero() || d.Sent.Before(firstSent) {
					firstSent = d.Sent
				}
				if d.Status == 200 && (firstOK.IsZero() || d.Recv.Before(firstOK)) {
					firstOK = d.Recv
				}
			}
			switch {
			case firstSent.IsZero() || dn.At.Before(firstSent):
				bad++
				r.fail("%s: the approval wait ended at %s, before any decision was sent (first %s)", inst, dn.At.Format("15:04:05.000"), firstSent.Format("15:04:05.000"))
			case !firstOK.IsZero():
				upper(inst, "approval wait", firstOK, dn.At)
			}
		}
	}
	r.Summary = fmt.Sprintf("%d sleeps, %d waits, %d early, %d late; longest finish after its earliest %.1fs", sleeps, waits, bad, late, worst.Seconds())
	return r
}

// checkW3 — approvals: one approval report per wait, one decision per
// approval, and the decision the Workflow sees is the one tailworks accepted,
// with the accepting caller's verified identity as its approver.
func checkW3(wh *wfHist, faults []faultEv, auditErr error) result {
	r := result{ID: "W3", Pass: true}
	x := indexWF(wh)
	requested := map[string]int{}
	decisions := map[string][]wfAudit{}
	for _, a := range wh.audits {
		switch a.Kind {
		case "approval_requested":
			requested[a.Instance]++
		case "approval_decision":
			decisions[a.Instance] = append(decisions[a.Instance], a)
		}
	}
	decided, doubles, bad := 0, 0, 0
	for _, inst := range x.insts {
		var oks []wfDecision
		for _, d := range x.decs[inst] {
			if d.Status == 200 {
				oks = append(oks, d)
			}
		}
		if len(oks) > 1 {
			doubles++
			var s []string
			for _, d := range oks {
				s = append(s, fmt.Sprintf("%s %s answered 200 at %s", d.Client, d.Decision, d.Recv.Format("15:04:05.000")))
			}
			r.fail("%s: %d decisions accepted for one approval: %s", inst, len(oks), strings.Join(s, "; "))
		}
		if n := len(decisions[inst]); n > 1 {
			bad++
			var s []string
			for _, a := range decisions[inst] {
				s = append(s, a.Decision+" by "+a.ApproverNodeID)
			}
			r.fail("%s: %d approval_decision audit records: %s", inst, n, strings.Join(s, ", "))
		}
		if n := requested[inst]; n > 1 {
			bad++
			r.fail("%s: the wait was reported %d times (approval_requested)", inst, n)
		}
		var dn *wfNote
		for _, n := range x.notes[inst] {
			if n.Kind != "decided" {
				continue
			}
			if dn == nil {
				n := n
				dn = &n
				continue
			}
			if n.Approved != dn.Approved || n.NodeID != dn.NodeID || n.Error != dn.Error {
				bad++
				r.fail("%s: a replay saw another decision: %+v, first %+v", inst, n, *dn)
			}
		}
		if dn == nil {
			continue
		}
		if dn.Error != "" {
			r.note("%s: the approval wait threw: %s", inst, dn.Error)
			continue
		}
		decided++
		if auditErr == nil && requested[inst] == 0 {
			// hostd sends its audit chain with each heartbeat: a crash
			// loses what it hadn't sent, and the chain shows the gap
			// (hostd/auditresume.go). The wait is still listed
			// (approvals.json); only its record is gone.
			aw, _ := x.firstNote(inst, "approval-wait")
			if k := crashNear(faults, x.storageHost(inst), aw.At); k != "" {
				r.note("%s: approval_requested lost in a %s just after the wait was reported (%s)", inst, k, aw.At.Format("15:04:05.000"))
			} else {
				bad++
				r.fail("%s: decided, but its wait was never reported (no approval_requested)", inst)
			}
		}
		match := func(d wfDecision) bool { return d.NodeID == dn.NodeID && (d.Decision == "approve") == dn.Approved }
		switch {
		case len(oks) >= 1:
			okMatch := false
			for _, d := range oks {
				okMatch = okMatch || match(d)
			}
			if !okMatch {
				bad++
				r.fail("%s: the Workflow saw approved=%v by %s, tailworks accepted %s %s", inst, dn.Approved, dn.NodeID, oks[0].Decision, oks[0].NodeID)
			}
		default:
			sent := false
			for _, d := range x.decs[inst] {
				sent = sent || match(d)
			}
			if !sent {
				bad++
				r.fail("%s: the Workflow saw approved=%v by %q, which no approver sent", inst, dn.Approved, dn.NodeID)
			}
		}
		if dn.Via != "tailnet" {
			bad++
			r.fail("%s: approver via %q, want tailnet", inst, dn.Via)
		}
		if ds := decisions[inst]; auditErr == nil && len(ds) == 1 && ds[0].ApproverNodeID != dn.NodeID {
			bad++
			r.fail("%s: audit says %s decided, the Workflow saw %s", inst, ds[0].ApproverNodeID, dn.NodeID)
		}
	}
	if auditErr != nil {
		r.fail("audit unreadable: %v", auditErr)
	}
	r.Summary = fmt.Sprintf("%d approvals decided, %d decided twice, %d other violations; %d approval_requested records", decided, doubles, bad, len(requested))
	return r
}

// crashNear names a fault that killed hostd on host within 10 s after t
// (before its next audit flush), or "".
func crashNear(faults []faultEv, host string, t time.Time) string {
	for _, f := range faults {
		if f.Host == host && (f.Kind == "kill-host" || f.Kind == "crash-hostd") && !f.Start.Before(t.Add(-time.Second)) && f.Start.Before(t.Add(10*time.Second)) {
			return f.Kind
		}
	}
	return ""
}

// checkW4 — moves: an instance lives on one host's storage, so it only ever
// acts there, and only while that host holds the lease (+5 s for a live
// move's fence). After a move it is visibly gone from the new copy (status
// not found). Resuming after the workload moved back is noted.
func checkW4(wh *wfHist, deps []depEv, faults []faultEv) result {
	r := result{ID: "W4", Pass: true}
	x := indexWF(wh)
	const fenceSlack = 5 * time.Second
	const activationLag = 10 * time.Second
	twoHosts, stale, resumed, gone, bad := 0, 0, 0, 0, 0
	for _, inst := range x.insts {
		acts := x.activity(inst)
		if len(acts) == 0 {
			continue
		}
		home := acts[0].host
		away := false // the lease left home since this instance last acted
		var lastAt time.Time
		for _, a := range acts {
			if a.host != home {
				twoHosts++
				r.fail("%s: acted on %s (%s at %s), its storage is on %s", inst, a.host, a.what, a.at.Format("15:04:05.000"), home)
				continue
			}
			// The watch sees a new copy only once mgmt reports it active, a
			// few seconds after it starts running: allow activationLag.
			if lh := leaseHost(deps, a.at); lh != home && lh != "" && leaseHost(deps, a.at.Add(-fenceSlack)) != home && !arrives(deps, home, a.at, a.at.Add(activationLag)) {
				stale++
				r.fail("%s: acted on %s (%s at %s) while the lease was on %s [fault %s]", inst, home, a.what, a.at.Format("15:04:05.000"), lh, faultAt(faults, a.at))
			}
			if !lastAt.IsZero() && leftHost(deps, home, lastAt, a.at) && !away {
				away = true
				resumed++
				r.note("%s: resumed on %s at %s (%s) after the workload had moved away (last acted %s)", inst, home, a.at.Format("15:04:05.000"), a.what, lastAt.Format("15:04:05.000"))
			}
			lastAt = a.at
		}
		f, ok := x.finals[inst]
		if !ok {
			continue
		}
		if home != wh.finalDep {
			gone++
			if f.Code != 404 {
				bad++
				r.fail("%s: its storage is on %s, the final copy is on %s, yet its status there is %d %q (want not found)", inst, home, wh.finalDep, f.Code, f.Status)
			}
		}
	}
	// A gone instance's approval stays listed on the host it was left on.
	stuckListed := 0
	for _, a := range wh.listed {
		if a.Status == "pending" && a.Host != wh.finalDep {
			stuckListed++
			r.note("%s: still listed as waiting for approval on %s; the workload runs on %s", a.Instance, a.Host, wh.finalDep)
		}
	}
	r.Summary = fmt.Sprintf("%d instances on another host than their storage, %d stale actions, %d resumed after a move back, %d gone from the final copy (%d still listed as waiting for approval), %d wrong final statuses",
		twoHosts, stale, resumed, gone, stuckListed, bad)
	return r
}

// checkW5 — convergence: every instance whose create was acknowledged (or
// that ran) ends complete, errored or terminated, or is gone from the final
// copy because its storage is on another host.
func checkW5(wh *wfHist, r result) result {
	x := indexWF(wh)
	counts := map[string]int{}
	for _, inst := range x.insts {
		c, created := x.created[inst]
		acted := len(x.activity(inst)) > 0
		if !(created && c.OK) && !acted {
			counts["indeterminate"]++
			continue
		}
		f, ok := x.finals[inst]
		home := x.storageHost(inst)
		switch {
		case !ok:
			counts["unread"]++
			r.fail("%s: no final status", inst)
		case f.Code == 200 && (f.Status == "complete" || f.Status == "terminated"):
			counts[f.Status]++
		case f.Code == 200 && f.Status == "errored":
			counts["errored"]++
			r.note("%s: errored: %s", inst, f.Error)
		case f.Code == 404 && home != wh.finalDep:
			counts["gone (storage on "+home+")"]++
		case f.Code == 404:
			counts["lost"]++
			r.fail("%s: not found on the final copy (%s), where its storage is", inst, home)
		default:
			counts["stuck"]++
			last := "nothing"
			if a := x.activity(inst); len(a) > 0 {
				la := a[len(a)-1]
				last = la.what + " at " + la.at.Format("15:04:05.000")
			}
			r.fail("%s: %d %q at the end (storage on %s; last activity: %s)", inst, f.Code, f.Status, home, last)
		}
	}
	var parts []string
	for k, v := range counts {
		parts = append(parts, fmt.Sprintf("%d %s", v, k))
	}
	sort.Strings(parts)
	r.Summary += "; " + strings.Join(parts, ", ")
	return r
}
