package hostd_test

import (
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestPublicIngressConcurrentApply (issue #133): the deploy that makes a
// public workload active and the assignment syncs that run alongside it
// both reconcile the public node. Run together they used to start it twice
// (the second saw the node but not yet its options, and restarted it), so
// two awaitNodes raced on the object (a data race under -race) and mgmt got
// two ready reports for one node. One start, one ready, however many syncs
// overlap the deploy.
func TestPublicIngressConcurrentApply(t *testing.T) {
	ff := &fakeFunnel{addrs: map[string]string{}}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) { o.PublicListen = ff.listen })
	e := s.e

	// Keep syncAssignments running back to back (each sync starts its own
	// applyPublic) for the whole deploy and the public node's join.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.Host.TriggerSync()
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	d := e.Deploy(hostdtest.Fixture("pubprobe"), "pubprobe", nil)
	e.WaitActive("pubprobe", d, 90*time.Second)
	var ps *hostd.PublicStatus
	deadline := time.Now().Add(60 * time.Second)
	for {
		ps = publicStatus(e, "pubprobe")
		if ps != nil && ps.Status == "ready" && ff.addr(ps.Hostname) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("public node not ready: %+v", ps)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Let the syncs keep overlapping a while after ready: a second start
	// would report ready again.
	time.Sleep(2 * time.Second)
	if n := e.FM.ReadyCalls(ps.ObjectID); n != 1 {
		t.Fatalf("public node reported ready %d times, want 1 (one node start per object start)", n)
	}
	if ps = publicStatus(e, "pubprobe"); ps == nil || ps.Status != "ready" || ps.Hostname != "pubprobe" || !ps.Exposed {
		t.Fatalf("public node after concurrent syncs: %+v", ps)
	}

	// Force the window: many reconciles at once, each seeing the node
	// without its recorded options. Exactly one restarts it.
	e.Host.ApplyPublicConcurrently("pubprobe", 8)
	deadline = time.Now().Add(60 * time.Second)
	for {
		ps = publicStatus(e, "pubprobe")
		if ps != nil && ps.Status == "ready" && e.FM.ReadyCalls(ps.ObjectID) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("public node not ready again after the restart: %+v (ready calls %d)", ps, e.FM.ReadyCalls(ps.ObjectID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	if n := e.FM.ReadyCalls(ps.ObjectID); n != 2 {
		t.Fatalf("after one restart the public node reported ready %d times, want 2", n)
	}
	if ps = publicStatus(e, "pubprobe"); ps == nil || ps.Status != "ready" {
		t.Fatalf("public node after concurrent reconciles: %+v", ps)
	}
}
