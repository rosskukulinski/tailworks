package hostd

import "context"

// consumeGate is the context every queue consumer loop runs under (#140).
//
// queueWanted stops the loops of a host that may have lost mgmt, but only
// at the next reconcile (queueReconcileEvery), and a loop's claim is a long
// poll (queuePollWait). Until then a self-fenced host could still claim, and
// with a live workerd consume, a message sent while it was cut off. Self-
// fencing cancels the gate (stopConsumers), which aborts every in-flight
// claim at once; no new gate is handed out until the host is unfenced.
type consumeGate struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// consumeContext is the context for a new consumer loop: cancelled when
// the host self-fences, and already cancelled while it is fenced.
func (h *Host) consumeContext() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fenced {
		ctx, cancel := context.WithCancel(h.ctx)
		cancel()
		return ctx
	}
	if h.consume.ctx == nil || h.consume.ctx.Err() != nil {
		h.consume.ctx, h.consume.cancel = context.WithCancel(h.ctx)
	}
	return h.consume.ctx
}

// stopConsumers cancels every consumer loop's in-flight claim. tick calls
// it on self-fencing, after setting h.fenced, so no loop started before can
// claim again and none can start until a heartbeat succeeds.
func (h *Host) stopConsumers() {
	h.mu.Lock()
	cancel := h.consume.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
