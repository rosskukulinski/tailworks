package mgmt

import (
	"context"
	"sync"
)

// Deploying a workload again after `tw undeploy` (issue #364).
//
// Undeploy kills every object of the workload, its entry node
// (namespace "_workload") included. Object IDs are deterministic
// (objectID: tenant, workload, namespace, name), so the next deploy under
// the same name asks for the same entry node, CreateObject found the
// killed row and answered 410, and the deploy failed with
// "bootstrap_failed: the workload node for <w> is killed" until
// `tw down --purge` forgot every killed object of the tenant.
//
// An undeploy ends the workload; deploying it again starts a new one. So
// when a deploy finds that the workload has no running deployment (its
// latest one is stopped, or it has none), it forgets the workload's killed
// and failed objects first, as `tw down --purge` does for the whole
// tenant: their pending device deletes run, then the rows and their kill
// revocations go, each audited as "purge". Every name the workload had,
// the entry node's and the names `tw kill` killed, can then be created
// again. While the workload stays deployed (redeploys, rollbacks, moves)
// a killed name stays killed. Live objects are never touched.

// workloadStopped reports whether workload has no running deployment: its
// latest deployment was undeployed, or it was never deployed.
func (m *Mgmt) workloadStopped(ctx context.Context, tenantID, workload string) bool {
	latest, err := m.store.LatestDeployments(ctx, tenantID)
	if err != nil {
		return false
	}
	for _, d := range latest {
		if d.Workload == workload {
			return d.StoppedAt != 0
		}
	}
	return true
}

// releaseUndeployedNames forgets the killed and failed objects of a
// workload that is being deployed again after an undeploy (a no-op while
// it is deployed). Deploy calls it once the lease is held, before the new
// deployment is recorded. It returns the IDs it forgot.
func (m *Mgmt) releaseUndeployedNames(ctx context.Context, tenantID, workload string) []string {
	if !m.workloadStopped(ctx, tenantID, workload) {
		return nil
	}
	dead, err := m.store.ListObjects(ctx, ObjectFilter{TenantID: tenantID, Workload: workload, Status: []string{StatusKilled, StatusFailed}})
	if err != nil {
		m.log.Warn("redeploy: listing the undeployed workload's killed objects failed", "workload", workload, "err", err)
		return nil
	}
	if len(dead) == 0 {
		return nil
	}
	// Their devices go first, in parallel: a new node of the same hostname
	// must not join while the old device still holds the name.
	var wg sync.WaitGroup
	for _, o := range dead {
		if o.DeviceDeleteAt <= 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.mu.Lock()
			pd := m.pending[o.ID]
			delete(m.pending, o.ID)
			m.mu.Unlock()
			if pd != nil {
				pd.timer.Stop()
			}
			if _, err := m.deleteDeviceClaimed(ctx, o, true); err != nil {
				m.log.Warn("redeploy: device delete of an undeployed object failed", "object", o.ID, "err", err)
			}
		}()
	}
	wg.Wait()
	l := m.tenantLock(tenantID)
	var gone []string
	for _, o := range dead {
		l.Lock()
		err := m.store.DeleteDeadObject(ctx, o.ID)
		l.Unlock()
		if err != nil {
			m.log.Warn("redeploy: forgetting an undeployed object failed", "object", o.ID, "err", err)
			continue
		}
		gone = append(gone, o.ID)
		m.mu.Lock()
		delete(m.waiters, o.ID)
		m.mu.Unlock()
		m.audit(ctx, AuditRecord{TenantID: tenantID, ObjectID: o.ID, Kind: "purge"},
			map[string]any{"workload": workload, "namespace": o.Namespace, "name": o.Name, "status": o.Status, "reason": "workload deployed again after undeploy"})
	}
	return gone
}
