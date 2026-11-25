package netd

import "context"

// gateEgress asks Config.EgressGate whether o may send now (issue #331).
//
// A node can send as soon as it has joined, a moment before its owner has
// told mgmt its stable node ID. mgmt issues the node certificate (ADR-010)
// on that report, and a receiver that looks the sender up first finds no
// certificate, so it would not narrow the sender's caps by the
// certificate's lim (RT-01). hostd's gate holds the egress until the
// report has been accepted. It is called after o.Ready, so o's stable node
// ID is known. The error is always a *Error (the callers type-assert it).
func (r *Registry) gateEgress(ctx context.Context, o *Object) error {
	g := r.cfg.EgressGate
	if g == nil {
		return nil
	}
	o.mu.Lock()
	node := string(o.nodeID)
	o.mu.Unlock()
	err := g(ctx, o.id, node)
	if err == nil {
		return nil
	}
	if e, ok := AsError(err); ok {
		c := *e // the callers set Object and Destination on it
		return &c
	}
	return newErr(CodeIdentityPending, err.Error())
}
