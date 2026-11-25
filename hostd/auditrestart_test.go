package hostd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestAuditChainAcrossRestart (#377): a clean hostd restart on the same
// data dir (tw down [--purge]; tw up) continues the host's audit chain at
// the head mgmt stored, so the chain still verifies. Before the fix every
// restart began a new segment at prevHash "" and tw audit reported
// host:h1 BROKEN at the first record after it. A restart whose saved head
// does not match mgmt's (records lost, or a chain changed at mgmt) still
// starts a new segment, and verification still reports it.
func TestAuditChainAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		between func(t *testing.T, e *hostdtest.Env)
		wantOK  bool
	}{
		{"clean restart", nil, true},
		{"saved head differs from mgmt's", func(t *testing.T, e *hostdtest.Env) {
			p := filepath.Join(e.DataDir(), "audit-head")
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("clean shutdown saved no chain head: %v", err)
			}
			os.WriteFile(p, []byte(strings.Repeat("ab", 32)+"\n"), 0o600)
		}, false},
		{"no saved head (crash)", func(_ *testing.T, e *hostdtest.Env) {
			os.Remove(filepath.Join(e.DataDir(), "audit-head"))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testAuditChainAcrossRestart(t, tc.between, tc.wantOK)
		})
	}
}

func testAuditChainAcrossRestart(t *testing.T, between func(*testing.T, *hostdtest.Env), wantOK bool) {
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
		onBackend(hostdtest.BackendReal, nil))
	e := s.e
	ctx := context.Background()
	// create makes an object and kills it: the host chains a kill record
	// (objects.go).
	create := func(name string) {
		t.Helper()
		d := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
		probe := e.WaitActive("probe", d, 60*time.Second)
		s.waitPeer(probe.IP)
		if code, r := s.getJSON("http://" + probe.IP + "/probe/" + name + "/"); code != 200 {
			t.Fatalf("create %s: %d %v", name, code, r)
		}
		o := e.WaitObject("probe", "Probe", name, 30*time.Second)
		if err := e.Mgmt.Do(ctx, "DELETE", "/v1/objects/"+o.ID+"?reason=test", nil, nil); err != nil {
			t.Fatalf("kill %s: %v", name, err)
		}
	}
	waitRecords := func(min int) mgmt.ChainStatus {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			st := e.RealMgmt.VerifyChain(ctx, "host:h1")
			if st.Records >= min {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("host chain has %d records, want ≥ %d: %+v", st.Records, min, st)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	create("a")
	if st := waitRecords(1); !st.OK {
		t.Fatalf("host chain before the restart: %+v", st)
	}
	before := e.RealMgmt.VerifyChain(ctx, "host:h1").Records

	if between != nil {
		e.RestartAfter(func() { between(t, e) })
	} else {
		e.Restart()
	}
	create("b")
	st := waitRecords(before + 1)
	switch {
	case wantOK && !st.OK:
		t.Fatalf("host chain after a clean restart: %+v (want verified)", st)
	case !wantOK && st.OK:
		t.Fatalf("host chain after a restart that did not continue it: %+v (want the new segment reported)", st)
	case !wantOK && st.BrokenAt != int64(before+1):
		t.Fatalf("host chain broken at %d, want %d (the first record after the restart): %+v", st.BrokenAt, before+1, st)
	}
}
