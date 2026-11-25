package hostd_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestRT3_H17_CronOverlap (security/LEDGER.md H17, issue #152): a
// scheduled() that leaves work running past the cron timeout (here in
// ctx.waitUntil(), after the handler itself has returned) must not overlap
// with the trigger's next run, and must not outlive the timeout. The run
// lasts until its waitUntil work settles, the next tick is skipped while it
// is in flight, and at CronTimeout hostd drops the run, which cancels it in
// workerd. The tick is driven with chosen wall-clock times (CronTickAt);
// everything below it is the production path.
//
// The fixture's waitUntil work holds until the test releases that run
// (POST /release), so neither check races the timeout. The overlap check
// runs under a timeout no step reaches. Only the cancellation check uses a
// short one. (With one short timeout for both, a slow /runs poll under load
// saw the first run start only after it had already timed out, and the next
// tick then rightly started a new run.)
func TestRT3_H17_CronOverlap(t *testing.T) {
	t.Parallel()
	const timeout = 2 * time.Second
	frozen := time.Date(2000, 1, 1, 0, 1, 0, 0, time.UTC) // see TestCronTriggers
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) {
		o.CronTimeout = time.Minute // the overlap check's run ends on release, not at this
		o.CronClock = func() time.Time { return frozen }
	})
	e := s.e
	// "1 0 4 1 *" records its start, then holds in waitUntil until its
	// scheduledTime is released and records its end.
	dep := e.Deploy(hostdtest.Fixture("cron"), "cron", nil)
	wl := e.WaitActive("cron", dep, 60*time.Second)
	s.waitPeer(wl.IP)

	// phases counts the start and end records of the run scheduled at at.
	phases := func(at time.Time) map[string]int {
		t.Helper()
		code, _, b := s.do("GET", "http://"+wl.IP+"/runs", nil)
		var out []map[string]any
		if code != 200 || json.Unmarshal(b, &out) != nil {
			t.Fatalf("/runs: %d %s", code, b)
		}
		n := map[string]int{}
		for _, r := range out {
			if r["cron"] == "1 0 4 1 *" && r["scheduledTime"] == float64(at.UnixMilli()) {
				p, _ := r["phase"].(string)
				n[p]++
			}
		}
		return n
	}
	release := func(at time.Time) {
		t.Helper()
		if code, _, b := s.do("POST", fmt.Sprintf("http://%s/release?t=%d", wl.IP, at.UnixMilli()), nil); code != 200 {
			t.Fatalf("/release: %d %s", code, b)
		}
	}

	// 1. No overlap: the trigger comes due again while the first run's
	// waitUntil work is held.
	at := time.Date(2027, 1, 4, 0, 1, 0, 0, time.UTC)
	first := make(chan int, 1)
	go func() { first <- e.Host.CronTickAt(at) }()
	deadline := time.Now().Add(30 * time.Second)
	for phases(at)["start"] == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first run never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.Host.CronTickAt(at.AddDate(1, 0, 0)); n != 0 {
		t.Fatalf("next tick started %d runs while the previous run was still in flight (overlap)", n)
	}
	select {
	case n := <-first:
		t.Fatalf("the run ended (%d started) while its waitUntil work was still held: the run must last until that settles", n)
	default:
	}
	release(at)
	if n := <-first; n != 1 {
		t.Fatalf("first tick started %d runs, want 1", n)
	}
	if p := phases(at); p["start"] != 1 || p["end"] != 1 {
		t.Fatalf("phases %v: want one start and one end for a released run", p)
	}

	// 2. A run whose work is never released is cancelled at the timeout.
	e.Host.SetCronTimeout(timeout)
	at2 := at.AddDate(2, 0, 0)
	st := time.Now()
	if n := e.Host.CronTickAt(at2); n != 1 {
		t.Fatalf("trigger stuck after a finished run: %d", n)
	}
	if d := time.Since(st); d < timeout {
		t.Fatalf("the run ended after %v, before its waitUntil work (the run must last until that settles or the %v timeout)", d, timeout)
	}
	if p := phases(at2); p["start"] != 1 {
		t.Fatalf("phases %v: the timed-out run never started", p)
	}
	// A run still going would see the release within one 50 ms poll and
	// record its end. A cancelled one records nothing.
	release(at2)
	time.Sleep(2 * time.Second)
	if p := phases(at2); p["end"] != 0 {
		t.Fatalf("phases %v: the timed-out run recorded its end after the timeout (it outlived its timeout)", p)
	}

	// 3. A cancelled run frees the trigger: the next due minute runs it.
	at3 := at.AddDate(3, 0, 0)
	release(at3)
	if n := e.Host.CronTickAt(at3); n != 1 {
		t.Fatalf("trigger stuck after a timed-out run: %d", n)
	}
	if p := phases(at3); p["end"] != 1 {
		t.Fatalf("phases %v: want the released run to finish", p)
	}
}
