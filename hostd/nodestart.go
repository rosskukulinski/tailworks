package hostd

// One node start, one awaitNode (issue #133).
//
// Every startNodeLocked bumps the object's nodeGen and hands the new value
// to its awaitNode. When a later start supersedes it (a public node
// restarted with new options, a rejoin after an undeploy), the older
// awaitNode sees a different nodeGen under h.mu and returns without touching
// the object: no status or MagicDNS write over the newer start's, no second
// mgmt Ready, no failure that marks the live node dead and retries it.
// netd's Create returns the existing node while it is pending or ready, so
// two starts can share one netd node and both see it become ready; only the
// newest reports it.

// bumpNodeGenLocked starts a new node generation for o and returns it.
// Any awaitNode of an earlier generation becomes a no-op. h.mu must be held.
func (o *object) bumpNodeGenLocked() uint64 {
	o.nodeGen++
	return o.nodeGen
}
