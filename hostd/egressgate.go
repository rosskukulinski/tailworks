package hostd

import (
	"context"
	"fmt"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd"
)

// Egress waits for the ready report (issue #331).
//
// netd lets a node send as soon as it has joined. hostd then tells mgmt the
// node's stable ID (the ready report, awaitNode/assignNode), and mgmt
// issues the object's node certificate on that report (ADR-010). A
// receiver that looks the sender up before then finds no certificate and
// treats it as a plain tailnet node: its caps are not narrowed by the
// certificate's lim (RT-01), and getCaller().tw is absent.
//
// So every egress of an object's node (netd's HTTP proxy and DialEgress:
// fetch, connect(), the Node tier's proxy, service bindings) waits until
// mgmt has accepted a ready report for the stable node ID it leaves from.
// The wait is bounded (reportWait). An egress is refused with
// identity_pending, never let through un-narrowed, when the wait runs out
// or the report failed. A failed report is re-sent on a later egress (at
// most every rereportEvery), so a transient mgmt failure heals without a
// node restart.
//
// The ready report never waits on the object's own egress, so holding it
// cannot deadlock: an object that calls out from its constructor just waits
// for the report like any other first call.

// reportWait bounds how long an egress waits for its object's ready report:
// the report's own 15 s timeout plus some slack.
// limits.md#egress-report-wait
const reportWait = 20 * time.Second

// rereportEvery is how often a failed ready report may be re-sent on
// behalf of a held egress.
const rereportEvery = 5 * time.Second

// readyReport is what hostd knows about mgmt's answers to an object's ready
// reports. Guarded by h.mu. It is not saved: after a restart every node
// reports again before it sends.
type readyReport struct {
	node   string        // the stable node ID of the last report mgmt accepted
	failed string        // the stable node ID of the last report that failed
	err    string        // why it failed (redacted)
	at     time.Time     // when failed was recorded, or a re-report started
	busy   bool          // a re-report is in flight (rereport)
	wake   chan struct{} // closed, and dropped, on every outcome
}

// egressGate is a tenant registry's netd.Config.EgressGate.
func (h *Host) egressGate(tenantID string) func(ctx context.Context, netdID, stableNodeID string) error {
	return func(ctx context.Context, netdID, stableNodeID string) error {
		return h.holdEgress(ctx, tenantID, netdID, stableNodeID)
	}
}

// holdEgress returns nil once mgmt has accepted a ready report for node,
// the stable ID object netdID sends from, and a *netd.Error otherwise.
func (h *Host) holdEgress(ctx context.Context, tenantID, netdID, node string) error {
	timer := time.NewTimer(reportWait)
	defer timer.Stop()
	for {
		if err := h.stallFencedEgress(netdID); err != nil { // stallfence.go (#501)
			return err
		}
		h.mu.Lock()
		o := h.byNetd[netdID]
		if o == nil || o.TenantID != tenantID {
			// Every node in a tenant registry is an object's (startNodeLocked).
			h.mu.Unlock()
			return &netd.Error{Code: netd.CodeUnknownObject, Detail: "egress from a node hostd has no object for"}
		}
		if o.Status == statusKilled {
			h.mu.Unlock()
			return &netd.Error{Code: netd.CodeKilled, Detail: "object " + o.ID + " was killed"}
		}
		r := &o.report
		if node != "" && r.node == node {
			h.mu.Unlock()
			return nil
		}
		if node == "" {
			h.mu.Unlock()
			return &netd.Error{Code: netd.CodeIdentityPending, Detail: "object " + o.ID + "'s node has no stable node ID yet"}
		}
		if r.failed == node && !r.busy {
			if time.Since(r.at) < rereportEvery || o.Status != statusReady || o.NodeID != node {
				err := r.err
				h.mu.Unlock()
				return &netd.Error{Code: netd.CodeIdentityPending, Detail: fmt.Sprintf(
					"mgmt has not accepted object %s's ready report (%s), so its node certificate is not issued and receivers could not apply its caps limit: egress refused until it is (retry)", o.ID, err)}
			}
			r.busy, r.at = true, time.Now()
			go h.rereport(o, node)
		}
		if r.wake == nil {
			r.wake = make(chan struct{})
		}
		ch := r.wake
		h.mu.Unlock()
		select {
		case <-ch:
		case <-timer.C:
			return &netd.Error{Code: netd.CodeIdentityPending, Detail: fmt.Sprintf(
				"object %s's ready report to mgmt has not completed after %v, so its node certificate is not issued yet: egress held, then refused (retry)", o.ID, reportWait)}
		case <-ctx.Done():
			return &netd.Error{Code: netd.CodeIdentityPending, Detail: "object " + o.ID + "'s ready report to mgmt has not completed (" + ctx.Err().Error() + ")"}
		}
	}
}

// noteReport records the outcome of a ready report of o for stableNodeID
// and wakes the egress waiting on it.
func (h *Host) noteReport(o *object, stableNodeID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &o.report
	if err == nil {
		r.node = stableNodeID
		if r.failed == stableNodeID {
			r.failed, r.err = "", ""
		}
	} else if r.node != stableNodeID { // mgmt still has the node an earlier report gave it
		r.failed, r.err, r.at = stableNodeID, netd.Redact(err.Error()), time.Now()
	}
	if r.wake != nil {
		close(r.wake)
		r.wake = nil
	}
}

// rereport re-sends o's ready report for node after one failed
// (holdEgress), with the lease epoch refreshed from mgmt.
func (h *Host) rereport(o *object, node string) {
	h.mu.Lock()
	req := mgmtapi.ReadyRequest{NodeID: node, StableNodeID: node, MagicDNS: o.MagicDNS}
	h.mu.Unlock()
	if w := h.workloadOf(o); w != nil {
		h.refreshEpoch(w) // readyretry.go
		req.Epoch = w.epochNow()
	}
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	err := h.mgmt.Ready(ctx, o.ID, req)
	cancel()
	h.mu.Lock()
	o.report.busy = false
	h.mu.Unlock()
	h.noteReport(o, node, err)
	if err == nil {
		h.log.Info("mgmt ready re-sent for a held egress", "object", o.ID)
		return
	}
	h.log.Warn("mgmt ready re-send failed", "object", o.ID, "err", err)
	if e, ok := mgmtapi.AsError(err); ok && e.Code == mgmtapi.CodeKilled {
		go h.killLocal(o.ID, "killed during join")
	}
}
