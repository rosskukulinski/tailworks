package mgmt

import (
	"context"
	"testing"
	"time"
)

// keepLease heartbeats host's lease on tenant's workload at epoch until the
// test ends, as hostd does while it runs the workload. Without it the
// lease lives NamespaceLeaseTTL from the deploy, so a test that runs longer
// than that (a loaded CI runner, -race) gets lease_lost mid-test. A
// renewal that fails (the test moved the lease away) is what hostd would
// see too; it changes nothing here.
func keepLease(t *testing.T, m *Mgmt, host, tenant, workload string, epoch int64) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(NamespaceLeaseTTL / 9)
		defer tick.Stop()
		lease := []HeartbeatLease{{Namespace: LeaseName(tenant, workload), Epoch: epoch}}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.Heartbeat(ctx, host, 0, lease)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}
