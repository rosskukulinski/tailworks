//go:build e2edataplane

package dataplane

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/cron"
)

// The cron loop's checkers (LOOP.md "Cron triggers and alarms", #419).
// Times are the recorder's receipt times and the test's clock; st is the
// run's scheduledTime, which hostd takes from its own wall clock (the same
// clock, until a clock-skew fault exists).

const (
	fenceAfter  = 30 * time.Second // hostd.DefaultFenceAfter
	tickEvery   = 5 * time.Second  // testdata/cron/cron.js: a held run reports this often
	reportDelay = 5 * time.Second  // a run's start report arrives at most this long after hostd decided to run it
	faultGrace  = 30 * time.Second // a host is serving its triggers again this long after a fault ends
)

// decided is when hostd decided to start the run, on the true clock: its
// scheduledTime, unless a clock fault had the host's clock off, when it is
// taken from the start report's receipt (at most reportDelay after).
func (r cronRunInfo) decided() time.Time {
	if r.Start.IsZero() || !r.skewed {
		return r.St
	}
	return r.Start.Add(-reportDelay)
}

// cronRunInfo is one run, from all its reports.
type cronRunInfo struct {
	WL, Cron, ID, Host string
	St                 time.Time
	Epoch              int64
	Start              time.Time // the start report's receipt (zero if lost)
	First, Last        time.Time // first and last report of any phase

	// skewed: the run started while a clock fault had the hosts' clock off.
	skewed bool
}

func (r cronRunInfo) String() string {
	return fmt.Sprintf("%s %q st %s on %s (epoch %d) reported %s–%s", r.WL, r.Cron, r.St.UTC().Format("15:04"), r.Host, r.Epoch,
		r.First.Format("15:04:05.000"), r.Last.Format("15:04:05.000"))
}

func cronRuns(ch *cronHist) []*cronRunInfo {
	by := map[string]*cronRunInfo{}
	var out []*cronRunInfo
	for _, e := range ch.runs {
		r := by[e.Run]
		if r == nil {
			r = &cronRunInfo{WL: e.WL, Cron: e.Cron, ID: e.Run, Host: hostOf(e.Node), St: time.UnixMilli(e.St), Epoch: e.Epoch, First: e.At, Last: e.At}
			by[e.Run] = r
			out = append(out, r)
		}
		if e.Phase == "start" {
			r.Start = e.At
		}
		if e.At.Before(r.First) {
			r.First = e.At
		}
		if e.At.After(r.Last) {
			r.Last = e.At
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First.Before(out[j].First) })
	for _, r := range out {
		for _, f := range ch.h.faults {
			if strings.HasPrefix(f.Kind, "clock-") && !r.First.Before(f.Start) && !r.First.After(f.End.Add(reportDelay)) {
				r.skewed = true
			}
		}
	}
	return out
}

// hostAt is where wl was last seen active at t (the watcher's view), and
// since when.
func hostAt(ch *cronHist, wl string, t time.Time) (string, time.Time) {
	host, since := "", time.Time{}
	for _, d := range ch.deps {
		if d.WL == wl && !d.At.After(t) && !d.At.Before(since) {
			host, since = d.Host, d.At
		}
	}
	return host, since
}

// watchLag: the watcher sees a new deployment active up to this long after
// its host starts acting (its poll period, plus the host's status report).
const watchLag = 3 * time.Second

// staleHost reports whether host had no business running wl at t: it is
// not where wl was seen active at any time in [t-slack, t+watchLag]. It
// returns where wl was then, and since when.
func staleHost(ch *cronHist, wl, host string, t time.Time, slack time.Duration) (bool, string, time.Time) {
	cur, since := hostAt(ch, wl, t.Add(-slack))
	if cur == "" || cur == host {
		return false, cur, since
	}
	for _, d := range ch.deps {
		if d.WL == wl && d.At.After(t.Add(-slack)) && !d.At.After(t.Add(watchLag)) && d.Host == host {
			return false, cur, since
		}
	}
	return true, cur, since
}

// cronMoveSlack: a live move (--replace) or an in-place redeploy lets the old
// copy run until its next heartbeat (docs/locks.md "Failure modes").
func cronMoveSlack(faults []faultEv, t time.Time) time.Duration {
	switch faultAt(faults, t) {
	case "move", "reschedule":
		return 5 * time.Second
	}
	return time.Second
}

// faultCovering names a fault whose window, widened to [Start-2s, End+grace],
// contains t (grace is 2 s for a reschedule, which has no outage), or "".
func faultCovering(faults []faultEv, t time.Time) string {
	for _, f := range faults {
		g := faultGrace
		if f.Kind == "reschedule" {
			g = 2 * time.Second
		}
		if !t.Before(f.Start.Add(-2*time.Second)) && !t.After(f.End.Add(g)) {
			return f.Kind + "@" + f.Host
		}
	}
	return ""
}

// schedAt is the set of crons wl must run for minute m: the schedule active
// then, or, while a redeploy is in progress, the crons both schedules share.
func schedAt(ch *cronHist, wl string, m time.Time) []string {
	var ss []schedEv
	for _, s := range ch.scheds {
		if s.WL == wl {
			ss = append(ss, s)
		}
	}
	var cur []string
	for i, s := range ss {
		if m.After(s.Active) {
			cur = s.Crons
			continue
		}
		if !m.Before(s.Sent) && i > 0 { // the redeploy is in progress
			var both []string
			for _, c := range s.Crons {
				if slices.Contains(ss[i-1].Crons, c) {
					both = append(both, c)
				}
			}
			return both
		}
		break
	}
	return cur
}

func matches(expr string, m time.Time) bool {
	s, err := cron.Parse(expr)
	return err == nil && s.Matches(m)
}

func checkCron(ch *cronHist) []result {
	runs := cronRuns(ch)
	return []result{checkC1(ch, runs), checkC2(ch, runs), checkC3(ch, runs), checkC4(ch, runs), checkA1(ch)}
}

// checkC1 — at most one run per (trigger, scheduled minute), across all
// hosts.
func checkC1(ch *cronHist, runs []*cronRunInfo) result {
	r := result{ID: "C1", Pass: true}
	by := map[string][]*cronRunInfo{}
	for _, x := range runs {
		k := fmt.Sprintf("%s %q %d", x.WL, x.Cron, x.St.UnixMilli())
		by[k] = append(by[k], x)
	}
	dups, known := 0, 0
	for k, xs := range by {
		if len(xs) < 2 {
			continue
		}
		var d []string
		for _, x := range xs {
			d = append(d, x.String())
		}
		msg := fmt.Sprintf("%s ran %d times: %s", k, len(xs), strings.Join(d, "; "))
		if issue := knownRerun(ch.h.faults, xs); issue != "" {
			known++
			r.note("known (%s): %s", issue, msg)
			continue
		}
		dups++
		r.fail("%s", msg)
	}
	r.Summary = fmt.Sprintf("%d runs of %d (trigger, minute) pairs, %d run more than once, %d known (#600: a clock step with a move)",
		len(runs), len(by), dups, known)
	return r
}

// knownRerun names the open issue a rerun of one minute falls under, or "":
// a clock step between the runs on two hosts (#600: cronLast is per host,
// so the host a workload moved to doesn't know what the old one ran). A
// rerun on one host after a restart was #594, fixed: it fails.
func knownRerun(faults []faultEv, xs []*cronRunInfo) string {
	first, last := xs[0].First, xs[0].First
	oneHost := true
	for _, x := range xs {
		oneHost = oneHost && x.Host == xs[0].Host
		if x.First.Before(first) {
			first = x.First
		}
		if x.First.After(last) {
			last = x.First
		}
	}
	stepped := false
	for _, f := range faults {
		if f.End.Before(first) || f.Start.After(last) {
			continue
		}
		if strings.HasPrefix(f.Kind, "clock-") {
			stepped = true
		}
	}
	switch {
	case !stepped:
		return ""
	case !oneHost:
		return "#600"
	}
	return ""
}

// checkC2 — no two runs of one trigger overlap: no run reports anything
// more than 1 s after a later run of the same trigger has started.
func checkC2(ch *cronHist, runs []*cronRunInfo) result {
	r := result{ID: "C2", Pass: true}
	by := map[string][]*cronRunInfo{}
	for _, x := range runs {
		by[x.WL+" "+x.Cron] = append(by[x.WL+" "+x.Cron], x)
	}
	overlaps, held, known := 0, 0, 0
	for _, xs := range by { // sorted by First
		for j, b := range xs {
			if b.Last.Sub(b.First) > 30*time.Second {
				held++
			}
			for _, a := range xs[:j] {
				if !a.Last.After(b.First.Add(time.Second)) {
					continue
				}
				kind := faultAt(ch.h.faults, b.First)
				msg := fmt.Sprintf("overlap: %s still reporting at %s, after %s started [fault %s]", a, a.Last.Format("15:04:05.000"), b, kind)
				if a.WL == cronSL && a.Host != b.Host && strings.HasSuffix(kind, "-takeover") {
					// A stateless workload's run on a cut-off or stalled host
					// goes on after the takeover (#566, needs a decision).
					known++
					r.note("known (#566): %s", msg)
					continue
				}
				overlaps++
				r.fail("%s", msg)
			}
		}
	}
	r.Summary = fmt.Sprintf("%d long runs (held past the next minute), %d overlaps, %d known (#566: a stateless run on a host taken over)", held, overlaps, known)
	return r
}

// checkC3 — fencing and liveness:
//   - no run starts on a host cut off from mgmt for longer than FenceAfter;
//   - no run starts on a host after the workload was seen active on
//     another one (a live move gets the old host's next heartbeat);
//   - every due minute runs, except while the trigger's previous run is in
//     flight or inside a fault window (plus faultGrace): skipped minutes
//     are bounded by the fault (a takeover's gap).
func checkC3(ch *cronHist, runs []*cronRunInfo) result {
	r := result{ID: "C3", Pass: true}
	faults := ch.h.faults
	fenced, stale := 0, 0
	for _, x := range runs {
		if x.Start.IsZero() {
			continue
		}
		for _, f := range faults {
			if (f.Kind == "cut-short" || f.Kind == "cut-takeover") && f.Host == x.Host &&
				x.decided().After(f.Start.Add(fenceAfter+time.Second)) && x.decided().Before(f.End) {
				fenced++
				r.fail("started past the fence: %s, cut off since %s (%s)", x, f.Start.Format("15:04:05.000"), f.Kind)
			}
		}
		decided := x.decided()
		if lb := x.Start.Add(-reportDelay); lb.After(decided) {
			decided = lb
		}
		if bad, cur, since := staleHost(ch, x.WL, x.Host, decided, cronMoveSlack(faults, decided)); bad {
			stale++
			r.fail("started on a host that no longer runs it: %s, decided ≥ %s, %s active on %s since %s [fault %s]",
				x, decided.Format("15:04:05.000"), x.WL, cur, since.Format("15:04:05.000"), faultAt(faults, decided))
		}
	}
	// Liveness.
	byMin := map[string]bool{}
	for _, x := range runs {
		byMin[fmt.Sprintf("%s %s %d", x.WL, x.Cron, x.St.UnixMilli())] = true
	}
	inFlight := func(wl, c string, m time.Time) bool {
		for _, x := range runs {
			if x.WL == wl && x.Cron == c && x.St.Before(m) && x.Last.After(m.Add(-tickEvery-time.Second)) {
				return true
			}
		}
		return false
	}
	due, ran, flight, inFaults, missing := 0, 0, 0, 0, 0
	from := ch.win.Start.Truncate(time.Minute).Add(time.Minute)
	for m := from; m.Add(15 * time.Second).Before(ch.win.End); m = m.Add(time.Minute) {
		for _, wl := range []string{cronSL, cronSF} {
			for _, c := range schedAt(ch, wl, m) {
				if !matches(c, m) {
					continue
				}
				due++
				switch {
				case byMin[fmt.Sprintf("%s %s %d", wl, c, m.UnixMilli())]:
					ran++
				case inFlight(wl, c, m):
					flight++
				case faultCovering(faults, m) != "":
					inFaults++
				default:
					missing++
					r.fail("skipped outside any fault: %s %q at %s", wl, c, m.UTC().Format("15:04"))
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("%d runs started past the fence, %d on a host that no longer ran the workload; %d due: %d ran, %d skipped (previous run in flight), %d skipped in fault windows, %d skipped otherwise",
		fenced, stale, due, ran, flight, inFaults, missing)
	return r
}

// checkC4 — a schedule change: after the new deployment is active, a
// trigger only the old schedule has never fires, and one only the new
// schedule has fires from the first matching minute.
func checkC4(ch *cronHist, runs []*cronRunInfo) result {
	r := result{ID: "C4", Pass: true}
	changes, old, late := 0, 0, 0
	for _, wl := range []string{cronSL, cronSF} {
		var ss []schedEv
		for _, s := range ch.scheds {
			if s.WL == wl {
				ss = append(ss, s)
			}
		}
		for i, s := range ss {
			until := ch.win.End
			if i+1 < len(ss) {
				until = ss[i+1].Sent
			}
			for _, x := range runs {
				if x.WL == wl && x.decided().After(s.Active) && x.decided().Before(until) && !slices.Contains(s.Crons, x.Cron) {
					old++
					r.fail("not in the schedule active since %s: %s", s.Active.Format("15:04:05.000"), x)
				}
			}
			if i == 0 {
				continue
			}
			changes++
			for _, c := range s.Crons {
				if slices.Contains(ss[i-1].Crons, c) {
					continue
				}
				m := s.Active.Truncate(time.Minute).Add(time.Minute)
				for !matches(c, m) {
					m = m.Add(time.Minute)
				}
				if !m.Before(until) || m.Add(15*time.Second).After(ch.win.End) {
					continue
				}
				found := false
				for _, x := range runs {
					if x.WL == wl && x.Cron == c && x.St.Equal(m) {
						found = true
					}
				}
				if !found {
					if f := faultCovering(ch.h.faults, m); f != "" && !strings.HasPrefix(f, "reschedule@") {
						r.note("%s %q's first minute %s skipped in a fault window (%s)", wl, c, m.UTC().Format("15:04"), f)
						continue
					}
					late++
					r.fail("%s %q, new at %s, did not run at %s", wl, c, s.Active.Format("15:04:05.000"), m.UTC().Format("15:04"))
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("%d schedule changes, %d runs of a trigger no longer scheduled, %d new triggers late", changes, old, late)
	return r
}

// checkA1 — alarms: an alarm never fires on a host after the workload was
// seen active on another one, and no alarm fires on two hosts; a set alarm
// fires at least once unless its storage was lost in a move (documented)
// or the chain was re-armed; the agent's scheduleEvery keeps firing
// outside fault windows.
func checkA1(ch *cronHist) result {
	r := result{ID: "A1", Pass: true}
	faults := ch.h.faults
	fired := map[string][]alarmEv{}
	stale, dup, lost, gaps, fires := 0, 0, 0, 0, 0
	for _, a := range ch.alarms {
		if a.Kind != "fire" {
			continue
		}
		fires++
		h := hostOf(a.Node)
		if bad, cur, since := staleHost(ch, cronSF, h, a.At, cronMoveSlack(faults, a.At)); bad {
			stale++
			r.fail("%s/%s fired on %s at %s (chain %s seq %d), %s active on %s since %s [fault %s]", a.Cls, a.Name, h,
				a.At.Format("15:04:05.000"), a.Chain, a.Seq, cronSF, cur, since.Format("15:04:05.000"), faultAt(faults, a.At))
		}
		if a.Cls != "sch" {
			k := fmt.Sprintf("%s/%s %s %d", a.Cls, a.Name, a.Chain, a.Seq)
			for _, p := range fired[k] {
				if hostOf(p.Node) != h {
					dup++
					r.fail("%s fired on %s at %s and on %s at %s", k, hostOf(p.Node), p.At.Format("15:04:05.000"), h, a.At.Format("15:04:05.000"))
				}
			}
			fired[k] = append(fired[k], a)
		}
	}
	// At least once.
	moved := func(after, before time.Time) bool {
		prev, _ := hostAt(ch, cronSF, after)
		for _, d := range ch.deps {
			if d.WL == cronSF && d.At.After(after) && d.At.Before(before) && d.Host != prev {
				return true
			}
		}
		return false
	}
	rearmed := func(a alarmEv, before time.Time) bool {
		for _, b := range ch.alarms {
			if b.Kind == "set" && b.Seq == 1 && b.Cls == a.Cls && b.Name == a.Name && b.Chain != a.Chain && b.At.After(a.At) && b.At.Before(before) {
				return true
			}
		}
		return false
	}
	sets := 0
	for _, a := range ch.alarms {
		if a.Kind != "set" {
			continue
		}
		sets++
		if len(fired[fmt.Sprintf("%s/%s %s %d", a.Cls, a.Name, a.Chain, a.Seq)]) > 0 {
			continue
		}
		deadline := a.At.Add(time.Duration(a.Delay)*time.Millisecond + 10*time.Second)
		for ext := true; ext; {
			ext = false
			for _, f := range faults {
				if !deadline.Before(f.Start.Add(-time.Second)) && deadline.Before(f.End.Add(faultGrace)) {
					deadline, ext = f.End.Add(faultGrace), true
				}
			}
		}
		if bad, _, _ := staleHost(ch, cronSF, hostOf(a.Node), a.At, cronMoveSlack(faults, a.At)); bad {
			continue // set by a copy that should have stopped: its fire is the stale one above
		}
		if deadline.After(ch.win.End) || moved(a.At.Add(-time.Second), deadline) || rearmed(a, deadline) {
			continue
		}
		lost++
		r.fail("%s/%s chain %s seq %d set at %s (due +%dms) never fired, and the object neither moved nor was re-armed by %s",
			a.Cls, a.Name, a.Chain, a.Seq, a.At.Format("15:04:05.000"), a.Delay, deadline.Format("15:04:05.000"))
	}
	// scheduleEvery(2 s): no gap over 10 s outside fault windows.
	by := map[string][]alarmEv{}
	for _, a := range ch.alarms {
		if a.Cls == "sch" && a.Kind == "fire" {
			k := a.Name + "@" + hostOf(a.Node)
			by[k] = append(by[k], a)
		}
	}
	for k, as := range by {
		sort.Slice(as, func(i, j int) bool { return as[i].At.Before(as[j].At) })
		for i := 1; i < len(as); i++ {
			p, n := as[i-1].At, as[i].At
			if n.Sub(p) <= 10*time.Second {
				continue
			}
			quiet := true
			for _, f := range faults {
				if p.Before(f.End.Add(faultGrace)) && n.After(f.Start.Add(-time.Second)) {
					quiet = false
				}
			}
			if quiet {
				gaps++
				r.fail("sch %s: no tick from %s to %s outside any fault", k, p.Format("15:04:05.000"), n.Format("15:04:05.000"))
			}
		}
	}
	r.Summary = fmt.Sprintf("%d fires, %d sets: %d stale fires, %d fired on two hosts, %d never fired, %d scheduleEvery gaps", fires, sets, stale, dup, lost, gaps)
	return r
}

// TestReplayCron re-runs the cron checkers on a recorded run
// (TW_DP_REPLAY=<results dir> of a cron or alarms scenario).
func TestReplayCron(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" {
		t.Skip("TW_DP_REPLAY not set")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ch := &cronHist{h: &history{}}
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
		var err error
		switch line.Type {
		case "cron":
			var e cronEv
			err = json.Unmarshal(line.Ev, &e)
			ch.runs = append(ch.runs, e)
		case "alarm":
			var e alarmEv
			err = json.Unmarshal(line.Ev, &e)
			ch.alarms = append(ch.alarms, e)
		case "sched":
			var e schedEv
			err = json.Unmarshal(line.Ev, &e)
			ch.scheds = append(ch.scheds, e)
		case "wldep":
			var e wlDep
			err = json.Unmarshal(line.Ev, &e)
			ch.deps = append(ch.deps, e)
		case "window":
			err = json.Unmarshal(line.Ev, &ch.win)
		case "fault":
			var e faultEv
			err = json.Unmarshal(line.Ev, &e)
			ch.h.faults = append(ch.h.faults, e)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if ch.win.End.IsZero() {
		t.Fatal("no window event: not a cron run, or it did not finish")
	}
	for _, r := range checkCron(ch) {
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
