package hostd

// Killed objects of a workload this host no longer runs (issue #364).
//
// killLocal keeps a killed object as a tombstone, and admit refuses its
// name while the tombstone is here (admitkill.go, #153). `tw undeploy`
// kills every object of the workload, and mgmt forgets them when the
// workload is deployed again (mgmt/redeploy.go), so the names can be
// created again. The host must forget its tombstones too, or a redeploy
// onto this host would still refuse every object name the undeploy
// killed. It does so once the workload is no longer assigned here: a
// tombstone only guards admissions, which need the workload to run here.
// mgmt stays the authority: a name it still keeps killed is refused by
// CreateObject (410) whatever the host remembers.

// forgetUnassignedTombstones drops the killed objects whose workload is not
// in assigned ("<tenant>/<workload>"), once their kill has finished, and
// saves objects.json if any went.
func (h *Host) forgetUnassignedTombstones(assigned map[string]bool) {
	h.mu.Lock()
	n := 0
	for _, o := range h.objects {
		if !o.terminal() || assigned[o.TenantID+"/"+o.Workload] {
			continue
		}
		if o.kill != nil {
			select {
			case <-o.kill.done:
			default:
				continue // still being killed: next sync
			}
		}
		h.forgetLocked(o)
		n++
	}
	h.mu.Unlock()
	if n > 0 {
		h.saveObjects()
		h.log.Info("forgot killed objects of workloads no longer assigned here", "objects", n)
	}
}
