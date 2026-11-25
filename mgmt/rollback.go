package mgmt

import (
	"context"
	"net/http"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/oci"
	"github.com/rosskukulinski/tailworks/internal/registry"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Rollback (ADR-046): redeploy the stored bundle of an earlier
// deployment as a new deployment. It is a deploy: the request goes through
// Mgmt.Deploy, so validation, placement (sticky unless Replace or Host),
// the lease at epoch+1, host fencing and every deploy refusal apply
// unchanged, and the route has deploy's role (admin). The only differences
// are where the bundle comes from and the audit kind ("rollback", with
// rollbackOf).

// RollbackRequest is POST /v1/tenants/{id}/workloads/{workload}/rollbacks.
type RollbackRequest struct {
	TenantID      string `json:"-"`
	Workload      string `json:"-"`
	DeploymentID  string `json:"deploymentID,omitempty"`
	Host          string `json:"host,omitempty"`
	Replace       bool   `json:"replace,omitempty"`
	ForceTakeover bool   `json:"forceTakeover,omitempty"`
	By            string `json:"-"`
}

// RollbackResult is the 202 body (Rollback).
type RollbackResult struct {
	RollbackOf string           `json:"rollbackOf"`
	Target     DeploymentRecord `json:"target"`
	Deployment deploymentView   `json:"deployment"`
}

// auditKind is the deploy's audit kind: "rollback" for a rollback,
// "promote" for a promotion (preview.go).
func (r DeployRequest) auditKind() string {
	if r.rollbackOf != "" {
		return "rollback"
	}
	if r.preview != nil && r.preview.promotedFrom != "" {
		return "promote"
	}
	return "deploy"
}

// auditDetail adds rollbackOf to a deploy's audit detail.
func (r DeployRequest) auditDetail(detail map[string]any) map[string]any {
	if r.rollbackOf != "" {
		detail["rollbackOf"] = r.rollbackOf
	}
	r.preview.auditDetail(detail) // preview.go
	return detail
}

// rollbackTarget resolves the deployment to roll back to: the one named,
// or the newest earlier one that ran, still has its bundle, and whose
// bundle differs from the current one.
func (m *Mgmt) rollbackTarget(ctx context.Context, r RollbackRequest) (DeploymentRecord, Deployment, error) {
	var none DeploymentRecord
	if _, err := m.store.GetTenant(ctx, r.TenantID); err != nil {
		return none, Deployment{}, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if !validName(r.Workload) {
		return none, Deployment{}, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	hs, err := m.historyStore()
	if err != nil {
		return none, Deployment{}, err
	}
	top, err := hs.DeploymentHistory(ctx, r.TenantID, r.Workload, Page{Limit: 1})
	if err != nil {
		return none, Deployment{}, err
	}
	if len(top) == 0 {
		return none, Deployment{}, errf(404, "unknown_workload", "no deployment of %s for tenant %s", r.Workload, r.TenantID)
	}
	cur := top[0]
	m.fillRecord(ctx, hs, &cur, cur.ID)
	keep := m.bundleRetention()
	pruned := func(d DeploymentRecord) error {
		return errf(409, "bundle_pruned", "the bundle of %s (%s) was pruned: mgmt keeps the bundles of the newest %d deployments of each workload; redeploy that version from source with tw deploy",
			d.ID, short(d.BundleSHA256), keep)
	}

	var target DeploymentRecord
	if r.DeploymentID != "" {
		target, err = hs.DeploymentRecordByID(ctx, r.DeploymentID)
		if err != nil || target.TenantID != r.TenantID || target.Workload != r.Workload {
			return none, Deployment{}, lookupErr(orNotFound(err), errf(404, "not_found", "%s is not a deployment of workload %s", r.DeploymentID, r.Workload))
		}
		m.fillRecord(ctx, hs, &target, cur.ID)
		switch {
		case target.ID == cur.ID:
			return none, Deployment{}, errf(409, "no_rollback_target", "%s is the current deployment of %s; name an earlier one (tw deployments %s)", target.ID, r.Workload, r.Workload)
		case target.BundlePruned:
			return none, Deployment{}, pruned(target)
		case !target.BundleRetained:
			return none, Deployment{}, errf(409, "no_rollback_target", "%s was deployed without a bundle; there is nothing to redeploy", target.ID)
		}
	} else {
		found := false
		pg := Page{Limit: 100, After: &PageKey{Created: cur.CreatedAt, ID: cur.ID}}
		for !found {
			recs, err := hs.DeploymentHistory(ctx, r.TenantID, r.Workload, pg)
			if err != nil {
				return none, Deployment{}, err
			}
			for i := range recs {
				d := recs[i]
				m.fillRecord(ctx, hs, &d, cur.ID)
				if !d.ranOK() || d.BundleSHA256 == "" || d.BundleSHA256 == cur.BundleSHA256 {
					continue
				}
				if d.BundlePruned {
					return none, Deployment{}, pruned(d)
				}
				target, found = d, true
				break
			}
			if len(recs) < pg.Limit {
				break
			}
			last := recs[len(recs)-1]
			pg.After = &PageKey{Created: last.CreatedAt, ID: last.ID}
		}
		if !found {
			return none, Deployment{}, errf(409, "no_rollback_target", "%s has no earlier deployment that ran with a different bundle; tw deployments %s lists its history", r.Workload, r.Workload)
		}
	}
	d, err := m.store.GetDeployment(ctx, target.ID)
	if err != nil {
		return none, Deployment{}, err
	}
	if len(d.Bundle) == 0 { // pruned between the two reads
		return none, Deployment{}, pruned(target)
	}
	return target, d, nil
}

// deployRequestFor is the deploy a rollback to d runs.
func (m *Mgmt) deployRequestFor(r RollbackRequest, d Deployment) DeployRequest {
	return DeployRequest{TenantID: r.TenantID, Workload: r.Workload, Bundle: d.Bundle, Namespaces: namespacesIfNoConfig(d),
		Host: r.Host, Replace: r.Replace, ForceTakeover: r.ForceTakeover, ContainerImages: builtFromPinned(r.TenantID, d.Containers),
		By: r.By, rollbackOf: d.ID}
}

// Rollback redeploys an earlier deployment's bundle through Deploy.
func (m *Mgmt) Rollback(ctx context.Context, r RollbackRequest) (DeploymentRecord, DeployResponse, error) {
	target, d, err := m.rollbackTarget(ctx, r)
	if err != nil {
		return target, DeployResponse{}, err
	}
	resp, err := m.Deploy(ctx, m.deployRequestFor(r, d))
	return target, resp, err
}

// PreflightRollback is the rollback's dry run: the target's bundle against
// the current policy (PreflightBundle, as POST /v1/deployments?dryRun).
func (m *Mgmt) PreflightRollback(ctx context.Context, r RollbackRequest) (PreflightResult, error) {
	_, d, err := m.rollbackTarget(ctx, r)
	if err != nil {
		return PreflightResult{}, err
	}
	return m.PreflightBundle(ctx, m.deployRequestFor(r, d))
}

// builtFromPinned turns the images a deployment pinned from the tenant's
// own registry namespace (the ones tw deploy built and pushed) back into
// ContainerImages, so a rollback runs the same image digests instead of
// needing a rebuild. External images are pinned again at rollback.
func builtFromPinned(tenantID string, cs []container.Class) map[string]BuiltImages {
	own := func(ref string) bool {
		p, err := oci.ParseRef(ref)
		return err == nil && p.Registry == registry.LocalRegistry && registry.Namespace(p.Repository) == tenantID
	}
	out := map[string]BuiltImages{}
	for _, c := range cs {
		var b BuiltImages
		if own(c.Image) {
			b.Image = c.Image
		}
		for n, ref := range c.Images {
			if own(ref) {
				if b.Images == nil {
					b.Images = map[string]string{}
				}
				b.Images[n] = ref
			}
		}
		if b.Image != "" || len(b.Images) > 0 {
			out[c.Class] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func orNotFound(err error) error {
	if err == nil {
		return ErrNotFound
	}
	return err
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "no hash"
	}
	return sha
}

func (a *v1api) CreateRollback(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.CreateRollbackParams) {
	var req RollbackRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.TenantID, req.Workload, req.By = id, workload, principalOf(r.Context()).Actor(r)
	if boolOf(params.DryRun) {
		res, err := a.s.M.PreflightRollback(r.Context(), req)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, res)
		return
	}
	target, resp, err := a.s.M.Rollback(r.Context(), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.deploymentView(r, resp.DeploymentID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	location(w, "/v1/deployments/"+resp.DeploymentID)
	writeJSON(w, 202, RollbackResult{RollbackOf: target.ID, Target: target, Deployment: v})
}
