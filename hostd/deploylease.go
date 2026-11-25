package hostd

import (
	"context"
	"errors"
	"slices"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// deploySecrets is withSecrets for a deploy, at the workload's current
// epoch. When mgmt refuses the grant with lease_lost it renews the lease
// once (renewDeployLease) and asks again.
//
// The case that needs it is a restart: a host that was down longer than
// the lease TTL (45 s) comes back to a lease that is still its own, at
// the epoch its assignment names, but expired. Only a heartbeat renews it
// (mgmt's RenewLease takes an expired lease that nobody else acquired),
// and the host's first heartbeat after a start goes out before it has any
// workload, so it renews nothing. The deploy's grant then came before the
// next heartbeat and failed, and a failed deployment is never retried, so
// the workload stayed failed. `tw upgrade` hits it whenever the store was
// restored from a backup older than the TTL (a rollback), or the restarts
// add up to more than that.
func (h *Host) deploySecrets(ctx context.Context, w *workload, app *bundle.App, tier string) (*bundle.App, error) {
	out, err := h.withSecrets(ctx, w, app, tier, w.epochNow())
	var e *mgmtapi.Error // withSecrets wraps it
	if !errors.As(err, &e) || e.Code != mgmtapi.CodeLeaseLost {
		return out, err
	}
	if !h.renewDeployLease(ctx, w) {
		return nil, err
	}
	h.log.Info("lease renewed for the deploy; asking for the secrets again", "workload", w.name, "epoch", w.epochNow())
	return h.withSecrets(ctx, w, app, tier, w.epochNow())
}

// renewDeployLease raises w's epoch to its assignment's (refreshEpoch,
// readyretry.go) and sends a heartbeat now, which renews the leases of the
// live deployments, w's included (it is "starting"). It reports whether
// mgmt renewed w's lease. The rest of the reply is left to the next
// regular heartbeat: acting on its revocations here could undeploy w,
// whose deploy holds w.deployMu.
func (h *Host) renewDeployLease(ctx context.Context, w *workload) bool {
	h.refreshEpoch(w)
	req := h.heartbeatRequest()
	if !slices.ContainsFunc(req.Leases, func(l mgmtapi.Lease) bool { return l.Namespace == w.lease() }) {
		return false // superseded or stopped meanwhile
	}
	resp, err := h.mgmt.Heartbeat(ctx, h.cfg.HostID, req)
	if err != nil {
		return false
	}
	return !slices.Contains(resp.Revoke, w.lease())
}
