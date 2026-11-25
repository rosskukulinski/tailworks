package mgmt

import (
	"context"
	"errors"
)

// A hand-back from the previous lease holder (#431).
//
// hostd claims a batch, and before queue() runs the workload's lease moves
// (a --replace, a takeover, a revoke). hostd sees the batch never ran and
// hands it back with a release-only settle (#388). Under the fence every
// other queue call gets, that host no longer holds the lease, so the
// release was refused (lease_lost). The batch then waited out the
// visibility timeout as though a consumer had crashed, and came back with
// an attempt it never ran; a message could reach its dead-letter queue
// with fewer real runs than max_retries+1.
//
// A release only gives a claim back: the message becomes visible at once
// and its claim's attempt is taken back. It reads nothing and settles
// nothing. The claim's lease ID (<id>:<token>, random, returned only to the
// claimer) already proves who made the claim, and SettleQueue ignores a
// lease that is no longer current (it counts as stale). So a release-only
// settle is accepted from the holder of an *older* epoch of the workload's
// lease. Acks, retries and extends keep the strict fence (fenceStore).

// ackRole is the role a settle needs: a release-only settle may come from
// an older epoch.
func ackRole(a QueueAckReq) queueRole {
	if len(a.Release) > 0 && len(a.Acks) == 0 && len(a.Retries) == 0 && len(a.Extend) == 0 {
		return queueAsReleaser
	}
	return queueAsConsumer
}

// fenceStaleRelease is fenceStore for a release-only settle: the caller's
// epoch may be older than the lease's, or the lease may have run out. A
// caller at the current epoch must be its holder, and an epoch from the
// future is refused.
func (m *Mgmt) fenceStaleRelease(ctx context.Context, tenantID, workload, hostID string, epoch *int64, what string) error {
	if epoch == nil {
		return m.fenceStore(ctx, tenantID, workload, hostID, epoch, what)
	}
	l, err := m.store.GetLease(ctx, LeaseName(tenantID, workload))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errf(409, "lease_lost", "no lease for %s/%s", tenantID, workload)
		}
		return err
	}
	if *epoch > l.Epoch || (*epoch == l.Epoch && hostID != "" && l.Holder != hostID) {
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "deny", Code: "lease_lost"},
			map[string]any{"host": hostID, "epoch": *epoch, "store": what})
		return errf(409, "lease_lost", "the lease on %s/%s was never held by %q at epoch %d", tenantID, workload, hostID, *epoch)
	}
	return nil
}
