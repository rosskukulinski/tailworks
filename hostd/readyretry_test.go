package hostd_test

import (
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestReadyRetriesAfterLeaseLost (#210): a workload node that joined, but
// whose ready report mgmt refused with lease_lost (a `tw deploy` bumped
// the lease epoch while a fresh hostd was still bringing up the previous
// deployment), is reported again with a refreshed epoch. Before the fix
// the node was ready on the host while mgmt's row stayed "minted", and
// `tw deploy` ended in identity_pending after 3 minutes.
func TestReadyRetriesAfterLeaseLost(t *testing.T) {
	t.Parallel()
	s := startSuite(t, fakemgmt.Config{NoPush: true})
	e := s.e
	e.FM.SetLeaseLostReady(1)
	st := time.Now()
	d := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	probe := e.WaitActive("probe", d, 60*time.Second)
	deadline := time.Now().Add(20 * time.Second)
	for {
		o, ok := e.FM.Object(probe.ID)
		if ok && o.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mgmt never saw %s ready after a lease_lost ready report: %+v", probe.ID, o)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.FM.ReadyCalls(probe.ID); n != 1 {
		t.Fatalf("accepted ready reports %d, want 1", n)
	}
	t.Logf("mgmt saw the node ready after a lease_lost retry in %v", time.Since(st))
}
