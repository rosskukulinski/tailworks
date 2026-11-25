package fakemgmt

import (
	"fmt"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// MoveWorkload moves a workload's current deployment to host to, as
// `tw deploy --replace --force-takeover` does after a host loss
// (ADR-028): a new deployment of the same bundle on to, holding the
// workload's lease at the next epoch. The old host is revoked on its next
// heartbeat (and fenced from mgmt at once). Tests of state that must
// survive a move. It returns the new deployment's ID.
func (s *Server) MoveWorkload(tenantID, workload, to string) (string, error) {
	s.mu.Lock()
	var cur *deployment
	for _, d := range s.deployments {
		if d.tenantID == tenantID && d.workload == workload && !d.superseded {
			cur = d
		}
	}
	h := s.hosts[to]
	if cur == nil || h == nil {
		s.mu.Unlock()
		return "", fmt.Errorf("fakemgmt: no deployment of %s/%s, or no host %q", tenantID, workload, to)
	}
	name := mgmtapi.LeaseName(tenantID, workload)
	epoch := s.leases[name].epoch + 1
	s.leases[name] = &lease{to, epoch}
	cur.superseded = true
	d := &deployment{tenantID: tenantID, workload: workload, bundle: cur.bundle, namespaces: cur.namespaces,
		wlTags: cur.wlTags, epoch: epoch, created: time.Now(), fence: cur.fence, containers: cur.containers, public: cur.public}
	d.DeploymentID, d.Host, d.Status = newID("dp"), to, "assigned"
	s.deployments[d.DeploymentID] = d
	push := h.PushURL
	if s.cfg.NoPush {
		push = ""
	}
	s.mu.Unlock()
	if push != "" {
		go s.push(push, "/v1/push/assignments", map[string]any{})
	}
	return d.DeploymentID, nil
}
