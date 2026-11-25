package mgmt

import (
	"context"
	"errors"
)

// killStalePoolNodes kills the workload's live pool nodes (ADR-031) that
// a host other than host warmed. A pre-joined node lives in one host's
// state directory and only the lease holder warms or hands out pool nodes,
// so once a deploy places the workload on host, another host's pool nodes
// can never be used again. Before this they stayed ready in mgmt with
// their devices registered on the tailnet after every move or takeover
// (#567): the old host has no lease to kill them with, the new one never
// knew them. The kill deletes the device, as for any object.
func (m *Mgmt) killStalePoolNodes(ctx context.Context, tenantID, workload, host, by string) {
	pool, err := m.store.ListObjects(ctx, ObjectFilter{TenantID: tenantID, Workload: workload, Namespace: PoolNamespace, Status: LiveStatuses})
	if err != nil {
		m.log.Warn("stale pool nodes: list", "workload", workload, "err", err)
		return
	}
	hostOf := map[string]string{} // deployment -> its host
	for _, o := range pool {
		h, ok := hostOf[o.DeploymentID]
		if !ok {
			if d, err := m.store.GetDeployment(ctx, o.DeploymentID); err == nil {
				h = d.HostID
			}
			hostOf[o.DeploymentID] = h
		}
		if h == "" || h == host {
			continue
		}
		if _, err := m.Kill(ctx, o.ID, KillRequest{Reason: "pool node of host " + h + ", which no longer runs the workload", By: by}); err != nil && !errors.Is(err, ErrNotFound) {
			m.log.Warn("stale pool nodes: kill", "object", o.ID, "host", h, "err", err)
		}
	}
}
