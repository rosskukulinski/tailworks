package hostd

import (
	"context"

	"github.com/rosskukulinski/tailworks/netd"
)

// closeNodes takes w's nodes off the tailnet (state kept, so they rejoin if
// the workload comes back here): netd resets every flow they carry.
//
// undeploy does this before it stops workerd when w fences (it holds
// Durable Objects, #572). Its lease is gone by then (revoked, fenced, or no
// longer assigned here), so another copy may already own the objects'
// state. Stopping workerd first gives it a graceful drain (SIGTERM, up to
// 3 s) during which it keeps answering what is already open: WebSocket
// messages on upgraded connections and in-flight requests, which the stall
// fence (stallfence.go) doesn't gate. A stateless workload keeps the drain.
func (h *Host) closeNodes(w *workload, t *tenant, reason string) {
	for _, o := range h.objectsOf(w.tenantID, w.name) {
		h.mu.Lock()
		live := o.live
		o.live = false
		if o.Status == statusReady || o.Status == statusPending {
			o.Status = statusPending
			o.readyCh = make(chan struct{})
		}
		h.mu.Unlock()
		if live {
			t.reg.Kill(context.Background(), o.NetdID, netd.KillOptions{Reason: reason, KeepState: true, SkipDelete: true})
		}
	}
}
