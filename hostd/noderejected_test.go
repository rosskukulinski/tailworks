package hostd_test

import (
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestReadyNodeRejectedRemints (issue #200): control rejects a workload node
// after it is ready (here its node key expires; a rejoin from the netmap
// cache sees the same NeedsLogin after Ready). hostd re-mints it under the
// same hostname, mgmt gets a ready report with the new node ID, and the
// workload answers on the new node.
func TestReadyNodeRejectedRemints(t *testing.T) {
	t.Parallel()
	s := startSuite(t, fakemgmt.Config{NoPush: true})
	e := s.e
	d := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	probe := e.WaitActive("probe", d, 60*time.Second)
	// hostd marks the object ready before its ready report reaches mgmt.
	waitMgmtNode := func(what string, ok func(nodeID string) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			o, found := e.FM.Object(probe.ID)
			if found && o.Status == "ready" && ok(o.NodeID) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: mgmt has %+v (first node %s)", what, o, probe.NodeID)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitMgmtNode("first ready report", func(id string) bool { return id == probe.NodeID })
	expired := false
	for _, n := range s.ctl.Server.AllNodes() {
		if n.Hostinfo.Valid() && n.Hostinfo.Hostname() == probe.Hostname {
			n.KeyExpiry = time.Now().Add(-time.Minute)
			s.ctl.Server.UpdateNode(n)
			expired = true
		}
	}
	if !expired {
		t.Fatalf("no control node for %s", probe.Hostname)
	}
	st := time.Now()
	waitMgmtNode("a ready node that control rejected was never re-minted", func(id string) bool { return id != "" && id != probe.NodeID })
	after := e.WaitActive("probe", d, 30*time.Second)
	t.Logf("re-minted and reported ready %v after the key expired", time.Since(st).Round(time.Millisecond))
	if after.Hostname != probe.Hostname || after.NodeID == probe.NodeID {
		t.Errorf("after: %+v (before %+v)", after, probe)
	}
	if n := e.FM.ReadyCalls(probe.ID); n != 2 {
		t.Errorf("accepted ready reports %d, want 2 (the first node, then the re-minted one)", n)
	}
	s.waitPeer(after.IP)
	if code, _ := s.getJSON("http://" + after.IP + "/probe/k/"); code != 200 {
		t.Fatalf("probe via the re-minted node: %d", code)
	}
}
