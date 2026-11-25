package mgmt

import (
	"context"
	"net/http"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Deployment history and bundle retention (ADR-046).
//
// Every deploy already left its deployment row behind; what was missing
// was a way to read a workload's history and a bound on the bundles mgmt
// keeps. Each deploy now records its bundle's SHA-256 (and, for a
// rollback, the deployment it redeployed), then prunes the workload's
// bundles beyond the newest Config.BundleRetention. A pruned deployment
// keeps its record and hash; only the bytes go, so it can no longer be
// rolled back to (rollback.go: 409 bundle_pruned).

// DefaultBundleRetention is how many bundles per workload mgmt keeps when
// Config.BundleRetention is 0 (tw-mgmt -bundle-retention).
const DefaultBundleRetention = 10

// DeploymentRecord is one entry of a workload's deployment history
// (GET /v1/tenants/{id}/workloads/{workload}/deployments).
type DeploymentRecord struct {
	ID         string `json:"deploymentID"`
	TenantID   string `json:"-"`
	Workload   string `json:"workload"`
	HostID     string `json:"host"`
	Epoch      int64  `json:"epoch"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	HostStatus string `json:"hostStatus,omitempty"`
	DeployedBy string `json:"deployedBy,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	ActiveAt   int64  `json:"activeAt,omitempty"`

	BundleSHA256   string `json:"bundleSHA256"`
	BundleBytes    int64  `json:"bundleBytes,omitempty"`
	BundleRetained bool   `json:"bundleRetained"`
	RollbackOf     string `json:"rollbackOf,omitempty"`

	StoppedAt    int64 `json:"-"`
	BundlePruned bool  `json:"-"` // retention dropped the bundle (vs. deployed without one)
}

// deployHistoryStore is the store side (sqlstore_deployhistory.go).
type deployHistoryStore interface {
	DeploymentHistory(ctx context.Context, tenantID, workload string, pg Page) ([]DeploymentRecord, error)
	DeploymentRecordByID(ctx context.Context, id string) (DeploymentRecord, error)
	SetDeploymentMeta(ctx context.Context, id, bundleSHA256, rollbackOf string) error
	MarkDeploymentActive(ctx context.Context, id, hostID string, now int64) error
	PruneBundles(ctx context.Context, tenantID, workload string, keep int, now int64) ([]string, error)
}

func (m *Mgmt) historyStore() (deployHistoryStore, error) {
	hs, ok := m.store.(deployHistoryStore)
	if !ok {
		return nil, errf(501, "internal", "this mgmt store keeps no deployment history")
	}
	return hs, nil
}

// bundleRetention is the effective retention (0: keep every bundle).
func (m *Mgmt) bundleRetention() int {
	switch r := m.cfg.BundleRetention; {
	case r == 0:
		return DefaultBundleRetention
	case r < 0:
		return 0
	default:
		return r
	}
}

// afterDeploy runs once a deployment is committed (Deploy): it records the
// bundle hash and rollbackOf, then prunes the workload's old bundles.
// Failures are logged, never fatal: the deploy has happened, history falls
// back to hashing a retained bundle, and the next deploy prunes again.
func (m *Mgmt) afterDeploy(ctx context.Context, d Deployment, rollbackOf string) {
	hs, err := m.historyStore()
	if err != nil {
		return
	}
	if err := hs.SetDeploymentMeta(ctx, d.ID, bundleSHA256(d.Bundle), rollbackOf); err != nil {
		m.log.Warn("deployment history", "deployment", d.ID, "err", err)
	}
	pruned, err := hs.PruneBundles(ctx, d.TenantID, d.Workload, m.bundleRetention(), m.now().UnixMilli())
	if err != nil {
		m.log.Warn("bundle retention", "workload", d.Workload, "err", err)
	}
	if len(pruned) > 0 {
		m.log.Info("bundles pruned", "tenant", d.TenantID, "workload", d.Workload, "deployments", pruned, "keep", m.bundleRetention())
	}
}

// noteDeploymentStatus records the first time a host reports a deployment
// active (setDepStatus): that is what "previous good" means for rollback.
func (m *Mgmt) noteDeploymentStatus(ctx context.Context, hostID, id string, st DeploymentStatus) {
	if st.Status != "active" {
		return
	}
	if hs, err := m.historyStore(); err == nil {
		if err := hs.MarkDeploymentActive(ctx, id, hostID, m.now().UnixMilli()); err != nil {
			m.log.Warn("deployment active", "deployment", id, "err", err)
		}
	}
}

// DeploymentHistory is a workload's deployments, newest first, with their
// status and whether their bundle is still held.
func (m *Mgmt) DeploymentHistory(ctx context.Context, tenantID, workload string, pg Page) ([]DeploymentRecord, error) {
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return nil, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	hs, err := m.historyStore()
	if err != nil {
		return nil, err
	}
	recs, err := hs.DeploymentHistory(ctx, tenantID, workload, pg)
	if err != nil {
		return nil, err
	}
	latest := ""
	if pg.After == nil && len(recs) > 0 {
		latest = recs[0].ID
	} else {
		top, err := hs.DeploymentHistory(ctx, tenantID, workload, Page{Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(top) > 0 {
			latest = top[0].ID
		}
	}
	if latest == "" {
		return nil, errf(404, "unknown_workload", "no deployment of %s for tenant %s", workload, tenantID)
	}
	for i := range recs {
		m.fillRecord(ctx, hs, &recs[i], latest)
	}
	return recs, nil
}

// fillRecord sets a record's status (as GetDeployment reports it) and
// backfills the hash of a retained bundle deployed before history existed.
func (m *Mgmt) fillRecord(ctx context.Context, hs deployHistoryStore, d *DeploymentRecord, latest string) {
	switch {
	case d.StoppedAt != 0:
		d.Status = "stopped"
	case d.ID != latest:
		d.Status = "superseded"
	case d.HostStatus == "":
		d.Status = "assigned"
	default:
		d.Status = d.HostStatus
	}
	if d.BundleSHA256 == "" && d.BundleRetained {
		if full, err := m.store.GetDeployment(ctx, d.ID); err == nil {
			d.BundleSHA256 = bundleSHA256(full.Bundle)
			hs.SetDeploymentMeta(ctx, d.ID, d.BundleSHA256, d.RollbackOf)
		}
	}
}

// ranOK reports whether a deployment served: the host reported it active
// (recorded since history exists, or still its last report for older
// records) and it did not end failed.
func (d DeploymentRecord) ranOK() bool {
	if d.HostStatus == "failed" {
		return false
	}
	return d.ActiveAt > 0 || d.HostStatus == "active" || d.HostStatus == "draining"
}

// historyPage is DeploymentHistoryPage.
type historyPage struct {
	Items           []DeploymentRecord `json:"items"`
	NextCursor      *string            `json:"nextCursor"`
	BundleRetention int                `json:"bundleRetention"`
}

func (a *v1api) ListWorkloadDeployments(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.ListWorkloadDeploymentsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	recs, err := a.s.M.DeploymentHistory(r.Context(), id, workload, pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p := mkPage(recs, n, func(d DeploymentRecord) PageKey { return PageKey{Created: d.CreatedAt, ID: d.ID} })
	writeJSON(w, 200, historyPage{Items: p.Items, NextCursor: p.NextCursor, BundleRetention: a.s.M.bundleRetention()})
}
