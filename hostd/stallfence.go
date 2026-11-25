package hostd

import (
	"fmt"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// The stall fence (#501).
//
// tick fences stateful workloads only when a heartbeat *fails* and the last
// success is older than FenceAfter. A host that was stopped (a paused VM, a
// frozen process, SIGSTOP) never runs that check: when it resumes, its first
// heartbeat succeeds and carries the revoke, and until that response has
// been handled nothing stopped the old copy. Its Durable Objects' alarms
// kept calling out, and egress that queued up behind a stopped hostd went
// out, seconds after another host had taken the lease over (docs/locks.md:
// 30 s fence < 45 s lease assumes the old host stops on its own clock).
//
// So a stateful workload's ingress and egress are gated on heartbeat
// freshness, the same test cron triggers use (cronLeaseFresh): with no
// successful heartbeat within FenceAfter (monotonic), they are refused at
// once, before any heartbeat. They pass again after a heartbeat succeeds,
// and tick records that success only after the heartbeat's revoke has been
// applied (heartbeat → revoke → undeploy), so a host whose lease moved
// never serves in between. Stateless workloads (fence=false) keep serving,
// as they do through a partition.

// stallFenced reports why w must not serve or send now, or "" if it may.
// w == nil (no workload) is never gated here.
func (h *Host) stallFenced(w *workload) string {
	if w == nil || h.cfg.FenceAfter <= 0 {
		return ""
	}
	h.mu.Lock()
	last, fenced := h.lastHB, h.fenced
	h.mu.Unlock()
	if last.IsZero() { // not started: nothing is deployed yet
		return ""
	}
	since := time.Since(last)
	if !fenced && since <= h.cfg.FenceAfter {
		return ""
	}
	if !w.fences() {
		return ""
	}
	return fmt.Sprintf("this host has had no successful heartbeat to mgmt for %v (fence window %v): it was stalled or cut off, so workload %s, which holds Durable Objects, is fenced until mgmt confirms it still holds the lease (retry; after a takeover the workload serves from its new host)",
		since.Round(time.Second), h.cfg.FenceAfter, w.name)
}

// stallFencedEgress is stallFenced for an egress of object netdID, as a
// netd error (holdEgress).
func (h *Host) stallFencedEgress(netdID string) error {
	h.mu.Lock()
	var w *workload
	if o := h.byNetd[netdID]; o != nil {
		w = h.workloads[o.TenantID+"/"+o.Workload]
	}
	h.mu.Unlock()
	if why := h.stallFenced(w); why != "" {
		return &netd.Error{Code: netd.CodeUpstreamUnavailable, Detail: "egress refused: " + why}
	}
	return nil
}
