package hostd_test

import (
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestRealMgmtStores runs the platform-store conformance tests (KV, D1,
// R2, Queues, secrets) with a real hostd against the real mgmt (package
// mgmt, in this process; hostdtest.BackendReal) instead of fakemgmt. The
// bodies are the fakemgmt tests' own (TestWorkersKV, TestD1, …), so a
// divergence between fakemgmt and mgmt fails one of the two runs (#108).
func TestRealMgmtStores(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, string)
	}{
		{"KV", testWorkersKV},
		{"D1", testD1},
		{"R2", testR2Binding},
		{"Queues", testQueues},
		{"Secrets", testWorkerSecrets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// In parallel with each other only (no other test in this
			// package is parallel): the run costs about as long as its
			// slowest body (Queues, ~30 s), not the sum.
			t.Parallel()
			tc.run(t, hostdtest.BackendReal)
		})
	}
}

// onBackend makes a suite option func select backend, then apply mod.
func onBackend(backend string, mod func(*hostdtest.Options)) func(*hostdtest.Options) {
	return func(o *hostdtest.Options) {
		o.Backend = backend
		if mod != nil {
			mod(o)
		}
	}
}

// startSuiteBackend is startSuite (same caps) on backend.
func startSuiteBackend(t *testing.T, fake fakemgmt.Config, backend string) *suite {
	return startSuiteCapsOpts(t, fake, tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
		mcpCap:          []tailcfg.RawMessage{`{"tools":["lookup_order"]}`},
	}, onBackend(backend, nil))
}
