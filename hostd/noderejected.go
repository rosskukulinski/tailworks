package hostd

// onNodeRejected is netd's Config.OnRejected (issue #200): control rejected
// one of this tenant's nodes after it was ready (a rejoin that went Running
// from tailscale's netmap cache before control answered, or a device
// removed or expired later). netd has already failed and closed that node
// and dropped its node state. Restart the object's node: the new join asks
// for a re-mint (keySource → mgmt Remint, which moves mgmt's row back to
// pending under the same hostname) and awaitNode reports the new node ID.
//
// Only a ready, live object is restarted. A killed or stopped one (its
// workload undeployed or fenced) is left alone: those set their own status
// first, so a rejection racing a kill does not bring the node back. The
// restart bumps nodeGen, so an older awaitNode or a pending retryReady
// (lease_lost, readyretry.go) for the rejected node stops instead of
// reporting it.
func (h *Host) onNodeRejected(tenantID, netdID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	o := h.byNetd[netdID]
	if o == nil || o.TenantID != tenantID || o.Status != statusReady || !o.live || o.terminal() {
		return
	}
	if t := h.tenants[tenantID]; t != nil && t.certs != nil && o.NodeID != "" {
		t.certs.forget(o.NodeID) // the old node's certificate must not be reused from cache
	}
	h.log.Warn("object node rejected by control after ready; re-minting under the same hostname", "object", o.ID, "hostname", o.Hostname, "node", o.NodeID)
	o.live = false
	o.Status, o.Err = statusPending, ""
	o.readyCh = make(chan struct{})
	h.startNodeLocked(o)
}
