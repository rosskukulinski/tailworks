package hostd

import "strings"

// Admissions do not outlive a kill (issue #153, ledger H19).
//
// A create admission is standing permission to mint a node for a name:
// a pending admits entry (RT3-06/07) or a public.create record
// (pubCreates, ADR-029 D5). ensure takes either one, whichever path
// reaches it: the request that was admitted, but also a plain DO's
// constructor reached through the isolate's admit cache, a raw binding, or
// a stub kept from before the kill. So an admission must not survive the
// object it was for:
//   - killLocal drops the name's pending admission and its public.create
//     record (whoever created it; a plain DO's ensure names no principal,
//     so the object's ActingFor is no guide);
//   - admit refuses a name that is killed on this host ("killed") until
//     it is purged, so nothing is admitted between the kill and the purge
//     for ensure to take afterwards (an undeployed workload's tombstones
//     go once it is no longer assigned here: unassignedtombstones.go);
//   - a tenant purge drops all of the tenant's admissions and records.

// dropAdmissionLocked forgets the pending create admission for o's name (h.mu held).
func (h *Host) dropAdmissionLocked(o *object) {
	delete(h.admits, o.key())
}

// killedLocked reports whether (tenant, workload, class, name) is a killed
// object this host still keeps as a tombstone (h.mu held). Killed objects
// leave byKey, so this scans; admit calls it only for names with no live
// object.
func (h *Host) killedLocked(key string) bool {
	for _, o := range h.objects {
		if o.terminal() && o.key() == key {
			return true
		}
	}
	return false
}

// killedName is the admit answer for a name that is killed on this host.
func killedName(class, name string) *apiError {
	return &apiError{Code: "killed", Detail: "object " + class + "/" + name + " was killed: it cannot be created again while its workload stays deployed (undeploy the workload and deploy it again, or tw down --purge)"}
}

// dropTenantAdmissions forgets every pending admission and public.create
// record of tenant id (its purge, #107).
func (h *Host) dropTenantAdmissions(id string) {
	h.mu.Lock()
	for k := range h.admits {
		if strings.HasPrefix(k, id+"|") {
			delete(h.admits, k)
		}
	}
	h.mu.Unlock()
	h.pubCreates.forgetTenant(id)
}

// forgetTenant drops every public.create record of tenant id.
func (pc *publicCreates) forgetTenant(id string) {
	if pc == nil {
		return
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	changed := false
	for k := range pc.Names {
		if strings.HasPrefix(k, id+"|") {
			delete(pc.Names, k)
			changed = true
		}
	}
	delete(pc.lane, id)
	if changed {
		pc.saveLocked()
	}
}
