package mgmt

import (
	"context"
	"testing"
	"time"
)

// keepHostsLive heartbeats the registered hosts ids (no leases, liveNodes 0)
// until the test ends, as real hosts do. Without it a host is live only for
// HostLeaseTTL after it registers, and a test that runs longer than that (a
// loaded CI runner, or many tests in parallel) gets 503 no_host "not live"
// (issue #183).
func keepHostsLive(t *testing.T, e *testEnv, ids ...string) {
	t.Helper()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(HostLeaseTTL / 10)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				for _, id := range ids {
					e.m.Heartbeat(context.Background(), id, 0, nil)
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}
