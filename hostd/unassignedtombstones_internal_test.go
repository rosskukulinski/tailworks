package hostd

import "testing"

// TestUndeployedTombstonesForgotten (issue #364): after `tw undeploy`, the
// host forgets the workload's killed objects once the workload is no longer
// assigned to it, so a redeploy onto the same host can admit those names
// again (mgmt has released them, mgmt/redeploy.go). While the workload is
// assigned, a killed name stays refused.
func TestUndeployedTombstonesForgotten(t *testing.T) {
	t.Parallel()
	h, inst, _ := admitKillHost(t)
	o := liveObject(h, "obj_r", "Room", "r")
	if err := h.killLocal(o.ID, "undeployed"); err != nil {
		t.Fatal(err)
	}
	admit := func() (int, map[string]any) {
		return platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "r", "as": map[string]any{"class": "Room", "name": "parent"}})
	}
	// Still assigned here: the kill stands.
	h.forgetUnassignedTombstones(map[string]bool{"tn/app": true})
	if code, r := admit(); code != 410 || r["code"] != "killed" {
		t.Fatalf("admit of a killed name while assigned: %d %v (want 410 killed)", code, r)
	}
	// Undeployed (not in the assignments any more): the tombstone goes.
	h.forgetUnassignedTombstones(map[string]bool{})
	h.mu.Lock()
	_, kept := h.objects[o.ID]
	h.mu.Unlock()
	if kept {
		t.Fatal("tombstone of an unassigned workload kept")
	}
	if code, r := admit(); code != 200 || r["existing"] != false {
		t.Fatalf("admit after the undeployed workload's tombstone went: %d %v (want 200, a new name)", code, r)
	}
}
