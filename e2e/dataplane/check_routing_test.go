//go:build e2edataplane

package dataplane

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The routing loop's checkers (#422, LOOP.md "Routing").

const (
	// bindCacheTTL is hostd's service-binding resolution cache
	// (hostd/servicebind.go svcTargetTTL, ADR-053 §3).
	bindCacheTTL = 5 * time.Second
	// liveMoveSlack: after a live move (--replace --force-takeover) or a
	// redeploy, the old copy stops at its host's next heartbeat
	// (docs/locks.md "Failure modes").
	liveMoveSlack = 5 * time.Second
	// bindRecoverBound: binding calls reach a moved target's new copy
	// within the cache TTL, plus PeerPathWait (netd's wait for a first path
	// to a new peer, 5 s) and slack, of it first answering directly.
	bindRecoverBound = bindCacheTTL + 15*time.Second
	// pathBound: a public route change applies within this long of the
	// new deployment being active.
	pathBound = 10 * time.Second
	// wsCloseBound: a WebSocket whose message goes unanswered is closed
	// within this long (FenceAfter 30 s plus a heartbeat and slack), or
	// within wsResumeBound of its host resuming or reconnecting.
	wsCloseBound  = 45 * time.Second
	wsResumeBound = 15 * time.Second
)

// rtFaultAt is the most recent fault on workload wl whose window (plus
// 15 s) contains t, or "".
func rtFaultAt(fs []rtFault, wl string, t time.Time) string {
	kind, start := "", time.Time{}
	for _, f := range fs {
		if f.Workload == wl && !t.Before(f.Start) && !t.After(f.End.Add(15*time.Second)) && f.Start.After(start) {
			kind, start = f.Kind, f.Start
		}
	}
	return kind
}

// answer is one reply from a copy of a workload.
type answer struct {
	path       string
	via        string
	sent, recv time.Time
	id         ident
}

// answers are every reply each workload's copies gave, by workload.
func answers(h *rhist) map[string][]answer {
	out := map[string][]answer{}
	for _, o := range h.ops {
		if o.A != nil && o.A.Epoch > 0 {
			out["rt-a"] = append(out["rt-a"], answer{o.Path, o.Via, o.Sent, o.Recv, *o.A})
		}
		if o.B != nil && o.B.Epoch > 0 {
			out["rt-b"] = append(out["rt-b"], answer{o.Path, o.Via, o.Sent, o.Recv, *o.B})
		}
		if o.W != nil && o.W.Epoch > 0 {
			out["rt-ws"] = append(out["rt-ws"], answer{o.Path, o.Via, o.Sent, o.Recv, *o.W})
		}
	}
	sent := map[string]time.Time{}
	for _, e := range h.ws {
		switch e.Kind {
		case "send":
			sent[fmt.Sprintf("%s/%d", e.Conn, e.S)] = e.At
		case "recv":
			if e.Epoch > 0 {
				s, ok := sent[fmt.Sprintf("%s/%d", e.Conn, e.S)]
				if !ok {
					s = e.At
				}
				out["rt-ws"] = append(out["rt-ws"], answer{"ws", e.Via, s, e.At, ident{Host: e.Host, Epoch: e.Epoch}})
			}
		}
	}
	return out
}

// checkN1 — no request (public, tailnet, binding, WebSocket) is answered by
// a copy past its fence: once a copy of a workload at a newer epoch has
// answered, no older epoch answers a request sent after that (a live move
// or a redeploy is allowed liveMoveSlack).
func checkN1(h *rhist) result {
	r := result{ID: "N1", Pass: true}
	stale, epochs := 0, 0
	byPath := map[string]int{}
	all := answers(h)
	for _, wl := range []string{"rt-a", "rt-b", "rt-ws"} {
		as := all[wl]
		first := map[int64]time.Time{}
		for _, a := range as {
			if f, ok := first[a.id.Epoch]; !ok || a.recv.Before(f) {
				first[a.id.Epoch] = a.recv
			}
		}
		epochs += len(first)
		allow := func(at time.Time) time.Duration {
			switch rtFaultAt(h.faults, wl, at) {
			case "move", "paths":
				return liveMoveSlack
			}
			return 0
		}
		for _, a := range as {
			for e, f := range first {
				if e > a.id.Epoch && a.sent.After(f.Add(allow(f))) {
					stale++
					byPath[a.path]++
					r.fail("%s: stale copy answered %s via %s: %s at epoch %d (sent %s), after epoch %d answered at %s [fault %s]",
						wl, a.path, a.via, a.id.Host, a.id.Epoch, a.sent.Format("15:04:05.000"), e, f.Format("15:04:05.000"), rtFaultAt(h.faults, wl, a.sent))
					break
				}
			}
		}
	}
	// After B moves, binding calls reach the new holder within the cache TTL
	// plus a first path to it: measured from the new copy's first direct
	// answer to the first binding answer from it, when nothing else
	// happens to A or B in between.
	direct, bind := map[int64]time.Time{}, map[int64]time.Time{}
	for _, a := range all["rt-b"] {
		m := direct
		if strings.HasPrefix(a.path, "bind-") || a.path == "public-bind" {
			m = bind
		}
		if f, ok := m[a.id.Epoch]; !ok || a.recv.Before(f) {
			m[a.id.Epoch] = a.recv
		}
	}
	var eps []int64
	for e := range direct {
		eps = append(eps, e)
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i] < eps[j] })
	quiet := func(from, to time.Time) bool {
		for _, f := range h.faults {
			if (f.Workload == "rt-a" || f.Workload == "rt-b" || f.Kind == "restart-mgmt") && f.End.After(from) && f.Start.Before(to) &&
				!(f.Workload == "rt-b" && f.Start.Before(from) && !f.End.After(from.Add(liveMoveSlack))) {
				return false
			}
		}
		return true
	}
	var recov []string
	slow := 0
	for _, e := range eps[1:] {
		f := direct[e]
		var b time.Time
		for e2, t := range bind {
			if e2 >= e && (b.IsZero() || t.Before(b)) {
				b = t
			}
		}
		switch {
		case !b.IsZero() && b.Sub(f) <= bindRecoverBound:
			recov = append(recov, fmt.Sprintf("%.1fs", max(0, b.Sub(f).Seconds())))
		case quiet(f.Add(-time.Second), f.Add(bindRecoverBound)):
			slow++
			got := "never"
			if !b.IsZero() {
				got = fmt.Sprintf("%.1fs later", b.Sub(f).Seconds())
			}
			r.fail("rt-b epoch %d answered directly at %s; binding calls reached it %s (bound %v)", e, f.Format("15:04:05.000"), got, bindRecoverBound)
		default:
			recov = append(recov, "(fault)")
		}
	}
	r.Summary = fmt.Sprintf("%d epochs answered, %d stale answers %v; bindings reached B's new copy after %v, %d too slow", epochs, stale, byPath, recov, slow)
	return r
}

// checkN2 — a revoked binding (its target undeployed) stops working within
// the cache bound: no binding call sent more than bindCacheTTL (+1 s) after
// the revocation is answered by the revoked copy.
func checkN2(h *rhist) result {
	r := result{ID: "N2", Pass: true}
	bas := answers(h)["rt-b"]
	late, direct := 0, 0
	var lastOK []string
	for _, rv := range h.revokes {
		// The epoch revoked: the newest B answered before the revocation.
		var e0 int64
		for _, a := range bas {
			if a.recv.Before(rv.At) && a.id.Epoch > e0 {
				e0 = a.id.Epoch
			}
		}
		var lastBind time.Time
		for _, a := range bas {
			if a.id.Epoch > e0 || !a.sent.After(rv.At) {
				continue
			}
			isBind := strings.HasPrefix(a.path, "bind-") || a.path == "public-bind"
			if isBind && a.recv.After(lastBind) {
				lastBind = a.recv
			}
			if !a.sent.After(rv.At.Add(bindCacheTTL + time.Second)) {
				continue
			}
			if isBind {
				late++
				r.fail("revoked binding still answered: %s sent %s (%.1fs after the revocation at %s) by B at epoch %d",
					a.path, a.sent.Format("15:04:05.000"), a.sent.Sub(rv.At).Seconds(), rv.At.Format("15:04:05.000"), a.id.Epoch)
			} else {
				direct++
				r.note("revoked B still answered %s directly, sent %.1fs after the revocation", a.path, a.sent.Sub(rv.At).Seconds())
			}
		}
		if !lastBind.IsZero() {
			lastOK = append(lastOK, fmt.Sprintf("%.1fs", lastBind.Sub(rv.At).Seconds()))
		} else {
			lastOK = append(lastOK, "none")
		}
	}
	r.Summary = fmt.Sprintf("%d revocations; last binding answer from the revoked copy after: %v; %d late binding answers, %d late direct", len(h.revokes), lastOK, late, direct)
	return r
}

// checkN3 — the identity a target sees is the real caller: B names the
// calling A copy's node on every binding call (fetch and RPC), and rt-cli
// on a direct call; A sees rt-cli on tailnet requests and a public
// requester on public ones.
func checkN3(h *rhist) result {
	r := result{ID: "N3", Pass: true}
	n, bad := 0, 0
	for _, o := range h.ops {
		if !o.OK {
			continue
		}
		fail := func(format string, a ...any) {
			bad++
			r.fail("%s sent %s via %s: %s", o.Path, o.Sent.Format("15:04:05.000"), o.Via, fmt.Sprintf(format, a...))
		}
		switch o.Path {
		case "bind-fetch", "bind-rpc", "public-bind":
			n++
			if o.A == nil || o.B == nil {
				fail("no answer from A or B")
			} else if o.B.Caller != o.A.Hostname {
				fail("B saw caller %q, A is %q (epoch %d)", o.B.Caller, o.A.Hostname, o.A.Epoch)
			}
		case "tailnet-b", "probe-b":
			n++
			if o.B.Caller != cliNode {
				fail("B saw caller %q, want %s", o.B.Caller, cliNode)
			}
		}
		switch o.Path {
		case "tailnet-a", "probe-a", "bind-fetch":
			n++
			if o.Req == nil || o.Req.Kind != "tailnet" || o.Req.Hostname != cliNode {
				fail("A saw requester %+v, want tailnet %s", o.Req, cliNode)
			}
		case "public", "public-bind", "probe-pub", "gate":
			n++
			if o.Req == nil || o.Req.Kind != "public" {
				fail("A saw requester %+v, want public", o.Req)
			}
		}
	}
	r.Summary = fmt.Sprintf("%d identities checked, %d wrong", n, bad)
	return r
}

// checkN4 — public path policy: a path that isn't listed never reaches
// workerd (A records every /gate hit before it answers), a tailnet-only
// path over the public listener is always 404 not_public, and a route
// change applies within pathBound of the new deployment being active.
func checkN4(h *rhist) result {
	r := result{ID: "N4", Pass: true}
	reached := 0
	for _, g := range h.gates {
		if g.Ver != "a-gate" {
			reached++
			r.fail("unlisted /gate request %s reached A's code (ver %s, %s epoch %d) at %s", g.ID, g.Ver, g.Host, g.Epoch, g.At.Format("15:04:05.000"))
		}
	}
	notPub := 0
	for _, o := range h.ops {
		if o.Path == "not-public" && (o.A != nil || o.Status == 200 || (o.Status != 0 && o.Code != "not_public")) {
			notPub++
			r.fail("tailnet-only path over the public listener: %d %s (sent %s via %s)", o.Status, o.Code, o.Sent.Format("15:04:05.000"), o.Via)
		}
	}
	// The listing in force at t: the newest rt-a deployment active for at
	// least pathBound.
	var deps []rtDep
	for _, d := range h.deps {
		if d.Workload == "rt-a" {
			deps = append(deps, d)
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].At.Before(deps[j].At) })
	listed := func(t time.Time) (ver string, settled bool) {
		for i := len(deps) - 1; i >= 0; i-- {
			if !deps[i].At.After(t) {
				settled = deps[i].At.Add(pathBound).Before(t) && (i+1 >= len(deps) || deps[i+1].At.After(t))
				return deps[i].Ver, settled
			}
		}
		return "", false
	}
	// changing: a route change is under way at t, or applied less than
	// pathBound ago (the deployment watcher can record it a poll late).
	changing := func(t time.Time) bool {
		for _, f := range h.faults {
			if f.Kind == "paths" && !t.Before(f.Start) && !t.After(f.End.Add(pathBound)) {
				return true
			}
		}
		return false
	}
	late, gateOps := 0, 0
	for _, o := range h.ops {
		if o.Path != "gate" || o.Status == 0 {
			continue
		}
		gateOps++
		ver, settled := listed(o.Sent)
		if !settled || inFault(h.faultEvs(), o.Sent, 0) || changing(o.Sent) {
			continue
		}
		switch {
		case ver == "a-nogate" && o.Status == 200:
			late++
			r.fail("/gate answered 200 %.1fs+ after the route was removed (sent %s via %s)", pathBound.Seconds(), o.Sent.Format("15:04:05.000"), o.Via)
		case ver == "a-gate" && o.Code == "not_public":
			late++
			r.fail("/gate refused not_public %.1fs+ after the route was added (sent %s via %s)", pathBound.Seconds(), o.Sent.Format("15:04:05.000"), o.Via)
		}
	}
	r.Summary = fmt.Sprintf("%d gate requests, %d unlisted reached workerd, %d tailnet-only answered publicly, %d route changes not applied within %v",
		gateOps, reached, notPub, late, pathBound)
	return r
}

// checkN5 — WebSockets: per connection, replies arrive in order with no
// duplicates and none skipped (a message sent on a connection that later
// answered a newer one was lost, hibernation included); a message that is
// never answered has its connection closed within wsCloseBound, or within
// wsResumeBound of its host's stall or cut ending.
func checkN5(h *rhist) result {
	r := result{ID: "N5", Pass: true}
	type conn struct {
		via   string
		sends map[int]time.Time
		recvs []wsEv
		end   *wsEv // close or stop
		opens time.Time
	}
	conns := map[string]*conn{}
	get := func(id string) *conn {
		if conns[id] == nil {
			conns[id] = &conn{sends: map[int]time.Time{}}
		}
		return conns[id]
	}
	var last time.Time
	for _, e := range h.ws {
		if e.At.After(last) {
			last = e.At
		}
		switch e.Kind {
		case "open":
			c := get(e.Conn)
			c.via, c.opens = e.Via, e.At
		case "send":
			get(e.Conn).sends[e.S] = e.At
		case "recv":
			c := get(e.Conn)
			c.recvs = append(c.recvs, e)
		case "close", "stop":
			c := get(e.Conn)
			if c.end == nil {
				ev := e
				c.end = &ev
			}
		}
	}
	// stallEnd: the end of a pause or cut of host that covers t, or zero.
	stallEnd := func(host string, t time.Time) time.Time {
		var end time.Time
		for _, f := range h.faults {
			if f.Host != host || t.Before(f.Start.Add(-time.Second)) || t.After(f.End) {
				continue
			}
			if strings.HasPrefix(f.Kind, "pause") || strings.HasPrefix(f.Kind, "cut") || f.Kind == "kill-host" || f.Kind == "crash-hostd" || f.Kind == "restart-mgmt" {
				if f.End.After(end) {
					end = f.End
				}
			}
		}
		return end
	}
	// crashed: host's hostd was killed (kill-host, crash-hostd) between
	// from and to.
	crashed := func(host string, from, to time.Time) bool {
		for _, f := range h.faults {
			if f.Host == host && (f.Kind == "kill-host" || f.Kind == "crash-hostd") && f.Start.After(from) && !f.Start.After(to) {
				return true
			}
		}
		return false
	}
	orphaned := map[string]bool{}
	msgs, answered, disorder, lost, holes, boots := 0, 0, 0, 0, 0, map[string]bool{}
	for _, id := range sortedKeys(conns) {
		c := conns[id]
		got := map[int]bool{}
		prev := 0
		for _, e := range c.recvs {
			boots[e.Boot] = true
			if got[e.S] {
				disorder++
				r.fail("%s: duplicate reply to %d at %s", id, e.S, e.At.Format("15:04:05.000"))
			} else if e.S < prev {
				disorder++
				r.fail("%s: reply to %d after %d at %s", id, e.S, prev, e.At.Format("15:04:05.000"))
			}
			got[e.S] = true
			if e.S > prev {
				prev = e.S
			}
		}
		for s, at := range c.sends {
			msgs++
			if got[s] {
				answered++
				continue
			}
			if s < prev {
				lost++
				r.fail("%s (via %s): message %d sent %s never answered, %d was", id, c.via, s, at.Format("15:04:05.000"), prev)
				continue
			}
			// Unanswered at the end of the connection: it must have closed in
			// time (not been black-holed).
			deadline := at.Add(wsCloseBound)
			if se := stallEnd(c.via, at); !se.IsZero() && se.Add(wsResumeBound).After(deadline) {
				deadline = se.Add(wsResumeBound)
			}
			switch {
			case c.end != nil && c.end.Kind == "close" && !c.end.At.After(deadline):
			case c.end != nil && c.end.Kind == "stop" && c.end.At.Before(deadline):
			case c.end == nil && last.Before(deadline):
			case crashed(c.via, c.opens, at):
				// The host crashed with the connection open: nothing on it
				// remembers the connection, so only a reset from the
				// restarted node (if the client reaches it before the node
				// moves away) or the client's own timeout or ping ends it.
				if !orphaned[id] {
					orphaned[id] = true
					r.note("%s (via %s): open across a host crash, unanswered from message %d (sent %s); ended %v", id, c.via, s, at.Format("15:04:05.000"), c.end != nil)
				}
			default:
				holes++
				endAt := "never"
				if c.end != nil {
					endAt = c.end.Kind + " at " + c.end.At.Format("15:04:05.000")
				}
				r.fail("%s (via %s): message %d sent %s unanswered and the connection not closed by %s (%s)",
					id, c.via, s, at.Format("15:04:05.000"), deadline.Format("15:04:05.000"), endAt)
			}
		}
	}
	delete(boots, "")
	r.Summary = fmt.Sprintf("%d connections, %d messages, %d answered, %d out of order or duplicated, %d lost, %d black-holed, %d left open across a host crash, %d object boots",
		len(conns), msgs, answered, disorder, lost, holes, len(orphaned), len(boots))
	return r
}

// checkN6 — availability per path, reported; gated (≥ 90%) only outside
// fault windows (+60 s), as D6 and multimgmt I8 are.
func checkN6(h *rhist) result {
	r := result{ID: "N6", Pass: true}
	const slack = 60 * time.Second
	fs := h.faultEvs()
	type tally struct{ in, inOK, out, outOK int }
	by := map[string]*tally{}
	add := func(path string, t time.Time, ok bool) {
		if by[path] == nil {
			by[path] = &tally{}
		}
		x := by[path]
		if inFault(fs, t, slack) {
			x.in++
			if ok {
				x.inOK++
			}
		} else {
			x.out++
			if ok {
				x.outOK++
			}
		}
	}
	for _, o := range h.ops {
		switch o.Path {
		case "public", "public-bind", "tailnet-a", "tailnet-b", "bind-fetch", "bind-rpc":
			add(o.Path, o.Sent, o.OK)
		}
	}
	got := map[string]bool{}
	for _, e := range h.ws {
		if e.Kind == "recv" {
			got[fmt.Sprintf("%s/%d", e.Conn, e.S)] = true
		}
	}
	for _, e := range h.ws {
		if e.Kind == "send" {
			add("ws", e.At, got[fmt.Sprintf("%s/%d", e.Conn, e.S)])
		}
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 100
		}
		return 100 * float64(a) / float64(b)
	}
	var parts []string
	for _, p := range sortedKeys(by) {
		x := by[p]
		parts = append(parts, fmt.Sprintf("%s %.0f%%/%d in, %.0f%%/%d out", p, pct(x.inOK, x.in), x.in, pct(x.outOK, x.out), x.out))
		if x.out >= 50 && pct(x.outOK, x.out) < 90 {
			r.fail("%s: only %.1f%% of %d ops outside fault windows succeeded", p, pct(x.outOK, x.out), x.out)
		}
	}
	// Recovery after each fault, per path: the first success after it ended.
	for _, f := range h.faults {
		var rec []string
		for _, p := range []string{"public", "tailnet-a", "tailnet-b", "bind-fetch", "bind-rpc"} {
			var first time.Time
			for _, o := range h.ops {
				if o.Path == p && o.OK && !o.Sent.Before(f.Start) && (first.IsZero() || o.Recv.Before(first)) {
					first = o.Recv
				}
			}
			if !first.IsZero() && first.After(f.End) {
				rec = append(rec, fmt.Sprintf("%s +%.1fs", p, first.Sub(f.End).Seconds()))
			}
		}
		if len(rec) > 0 {
			r.note("after %s of %s on %s: %s", f.Kind, f.Workload, f.Host, strings.Join(rec, ", "))
		}
	}
	r.Summary = strings.Join(parts, "; ")
	return r
}

// TestRoutingReplay re-runs the routing checkers on a recorded run
// (TW_DP_REPLAY=<results dir>).
func TestRoutingReplay(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" {
		t.Skip("TW_DP_REPLAY not set")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := &rhist{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var line struct {
			Type string
			Ev   json.RawMessage
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		dec := func(v any) {
			if err := json.Unmarshal(line.Ev, v); err != nil {
				t.Fatal(err)
			}
		}
		switch line.Type {
		case "op":
			var o rtOp
			dec(&o)
			h.ops = append(h.ops, o)
		case "ws":
			var o wsEv
			dec(&o)
			h.ws = append(h.ws, o)
		case "gate":
			var o gateHit
			dec(&o)
			h.gates = append(h.gates, o)
		case "fault":
			var o rtFault
			dec(&o)
			h.faults = append(h.faults, o)
		case "deployment":
			var o rtDep
			dec(&o)
			h.deps = append(h.deps, o)
		case "revoke":
			var o rtRevoke
			dec(&o)
			h.revokes = append(h.revokes, o)
		}
	}
	for _, r := range []result{checkN1(h), checkN2(h), checkN3(h), checkN4(h), checkN5(h), checkN6(h)} {
		mark := "✓"
		if !r.Pass {
			mark = "✗"
			t.Fail()
		}
		t.Logf("%s %s: %s", mark, r.ID, r.Summary)
		for _, e := range r.Evidence {
			t.Logf("    %s", e)
		}
		for _, e := range r.Notes {
			t.Logf("    (note) %s", e)
		}
	}
}
