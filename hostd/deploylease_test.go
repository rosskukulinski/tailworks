package hostd_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// offsetClock is the real time plus an offset a test moves forward.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) Now() time.Time                         { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *offsetClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (c *offsetClock) advance(d time.Duration)                { c.off.Add(int64(d)) }

// TestRedeployAfterLeaseExpired: a host that was down longer than the
// lease TTL redeploys its assignment when it comes back. The lease is still
// its own at the assignment's epoch, but expired; before deploylease.go the
// deploy's secret grant came before any heartbeat had renewed it, got
// lease_lost, and the deployment stayed failed (seen in e2e-tw-upgrade
// after a rollback restored a store older than the TTL).
//
// Not parallel: with heartbeats off, h1 is live only for HostLeaseTTL after
// it registers, so the first deploy must land within that window, which a
// CPU shared with the parallel tests can miss (no_host "h1: not live").
func TestRedeployAfterLeaseExpired(t *testing.T) {
	clk := &offsetClock{}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
		onBackend(hostdtest.BackendReal, func(o *hostdtest.Options) {
			o.MgmtClock = clk
			o.HeartbeatEvery = time.Hour // no heartbeat renews the lease behind the test's back
		}))
	e := s.e
	putSecret(t, e, "secrets", "API_KEY", testSecret)
	dep := e.Deploy(hostdtest.Fixture("secrets"), "secrets", nil)
	e.WaitActive("secrets", dep, 60*time.Second)

	// The host is down for a minute (> the 45 s lease TTL), then restarts.
	clk.advance(time.Minute)
	e.Restart()
	wl := e.WaitActive("secrets", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	if n := e.Hooks.SecretGrants(); n != 2 {
		t.Fatalf("secret grants %d, want 2 (the deploy and the redeploy)", n)
	}
}
