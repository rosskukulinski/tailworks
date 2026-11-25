package mgmt

import (
	"context"
	"net/http"
	"slices"
	"strings"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Preview deploys and promotion (ADR-048).
//
// `tw deploy --preview` (POST /v1/deployments with "preview": true) runs a
// bundle as a workload of its own, PreviewName(workload, alias) =
// "<workload>--preview-<alias>", next to the workload, which it never
// touches. That keeps every per-workload mechanism unchanged, as for
// replicas: the preview has its own namespace lease (so it never takes the
// workload's Durable Object lease or fences its host), deployment record,
// assignment, workload node and identity, Durable Object storage and
// platform stores (KV, R2, D1 start empty). The _workload namespace records
// the workload it previews (ClassConfig.PreviewOf).
//
// A preview has the workload's tags (callers reach it under the same
// grants) and its secrets (as replicas do, and as Cloudflare preview
// versions do), but none of what routes production traffic: no Tailscale
// Service, no public routes, and one replica. Queues are tenant-wide, so a
// preview would consume or produce the workload's messages: a bundle with
// queue bindings is refused (preview_unsupported).
//
// `tw promote <workload> <alias>` deploys the preview's bundle to the
// workload through Deploy, exactly as a rollback does (rollback.go): lease
// at epoch+1, host fencing, sticky placement, the workload's own replicas,
// Service and public routes. Then it undeploys the preview. Data never
// moves: the workload keeps its own storage.

const previewSep = "--preview-"

// PreviewName is the workload name of workload's preview alias.
func PreviewName(workload, alias string) string { return workload + previewSep + alias }

// previewOf returns the workload a deployment is a preview of ("" for any
// other deployment).
func previewOf(classes []ClassConfig) string {
	for _, c := range classes {
		if c.Class == WorkloadNamespace {
			return c.PreviewOf
		}
	}
	return ""
}

// previewCtx tells Deploy that it deploys a preview (of set) or promotes
// one (promotedFrom set). nil for every other deploy.
type previewCtx struct {
	of           string // the workload this deployment previews
	alias        string
	promotedFrom string // promote: the preview deployment whose bundle is deployed
	from         string // promote: the preview's workload name
}

// strip drops what routes production traffic from a preview's config:
// the Tailscale Service, public routes and replicas.
func (pc *previewCtx) strip(cfg *TwConfig) {
	if pc == nil || pc.of == "" || cfg == nil {
		return
	}
	cfg.Public, cfg.Tailnet.Service, cfg.Replicas = nil, nil, 0
	for k, o := range cfg.Tailnet.Objects {
		o.Public = nil
		cfg.Tailnet.Objects[k] = o
	}
}

// mark records the previewed workload on the _workload namespace (and
// clears it on a promotion). A nil receiver is a no-op.
func (pc *previewCtx) mark(classes []ClassConfig) {
	if pc == nil {
		return
	}
	for j := range classes {
		if classes[j].Class == WorkloadNamespace {
			classes[j].PreviewOf = pc.of
		}
	}
}

// auditDetail adds previewOf (a preview) or promotedFrom (a promotion).
func (pc *previewCtx) auditDetail(detail map[string]any) {
	switch {
	case pc == nil:
	case pc.of != "":
		detail["previewOf"], detail["alias"] = pc.of, pc.alias
	case pc.promotedFrom != "":
		detail["promotedFrom"], detail["preview"] = pc.promotedFrom, pc.from
	}
}

// deployPreview is Deploy's preview entry point. It handles a deploy with
// Preview (or PreviewAlias) set, and a redeploy of an existing preview by
// its own name (host drain, `tw deploy --name web--preview-x`), which
// stays a preview. Anything else is not handled (false).
func (m *Mgmt) deployPreview(ctx context.Context, r DeployRequest) (bool, DeployResponse, error) {
	var none DeployResponse
	if !r.Preview && r.PreviewAlias == "" {
		if !strings.Contains(r.Workload, previewSep) {
			return false, none, nil
		}
		d, err := m.latestDeployment(ctx, r.TenantID, r.Workload)
		of := previewOf(d.Namespaces)
		if err != nil || of == "" {
			return false, none, nil
		}
		resp, err := m.deployAsPreview(ctx, r, &previewCtx{of: of, alias: strings.TrimPrefix(r.Workload, PreviewName(of, ""))})
		return true, resp, err
	}
	if _, err := m.store.GetTenant(ctx, r.TenantID); err != nil {
		return true, none, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if !validName(r.Workload) {
		return true, none, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	alias := r.PreviewAlias
	if alias == "" {
		alias = bundleSHA256(r.Bundle)[:8]
	}
	if !validName(alias) || len(alias) > 20 {
		return true, none, errf(400, "bad_request", "preview alias %q must be a DNS label of at most 20 characters ([a-z0-9-])", alias)
	}
	name := PreviewName(r.Workload, alias)
	if !validName(name) {
		return true, none, errf(400, "bad_request", "the preview's workload name %s is longer than 63 characters; use a shorter --preview-alias or workload name", name)
	}
	if strings.Contains(r.Workload, previewSep) {
		return true, none, errf(400, "preview_unsupported", "%s is a preview itself; preview the workload it previews", r.Workload)
	}
	if d, err := m.latestDeployment(ctx, r.TenantID, r.Workload); err == nil && (replicaGroup(d.Namespaces) != "" || previewOf(d.Namespaces) != "") {
		return true, none, errf(400, "preview_unsupported", "%s is a replica or preview of another workload; preview that workload instead", r.Workload)
	}
	if d, err := m.latestDeployment(ctx, r.TenantID, name); err == nil && d.StoppedAt == 0 && previewOf(d.Namespaces) != r.Workload {
		return true, none, errf(409, "preview_conflict", "workload %s exists and is not a preview of %s; pick another --preview-alias", name, r.Workload)
	}
	of := r.Workload
	r.Workload = name
	resp, err := m.deployAsPreview(ctx, r, &previewCtx{of: of, alias: alias})
	return true, resp, err
}

// deployAsPreview runs the inner Deploy of preview pc (one replica, no
// replica group).
func (m *Mgmt) deployAsPreview(ctx context.Context, r DeployRequest, pc *previewCtx) (DeployResponse, error) {
	if len(r.Bundle) > 0 {
		q, err := BundleQueues(r.Bundle)
		if err != nil {
			return DeployResponse{}, errf(400, "bad_bundle", "%v", err)
		}
		if !q.Empty() {
			return DeployResponse{}, errf(400, "preview_unsupported", "the bundle binds queues, which are shared by the whole tenant: a preview would send to and consume %s's messages. Deploy it as a workload of its own (--name) instead", pc.of)
		}
	}
	r.Preview, r.PreviewAlias = false, ""
	r.preview = pc
	r.replica = &replicaCtx{n: 1}
	return m.Deploy(ctx, r)
}

// Preview is one running preview (GET …/previews).
type Preview struct {
	Workload     string         `json:"workload"`
	Alias        string         `json:"alias"`
	PreviewOf    string         `json:"previewOf"`
	BundleSHA256 string         `json:"bundleSHA256,omitempty"`
	Deployment   deploymentView `json:"deployment"`
}

// previewDeployments are the running previews of workload, newest first.
func (m *Mgmt) previewDeployments(ctx context.Context, tenantID, workload string) ([]Deployment, error) {
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return nil, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if !validName(workload) {
		return nil, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	latest, err := m.store.LatestDeployments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []Deployment
	for _, d := range latest {
		if d.StoppedAt == 0 && previewOf(d.Namespaces) == workload {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b Deployment) int {
		if a.CreatedAt != b.CreatedAt {
			return int(b.CreatedAt - a.CreatedAt)
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

// PromoteRequest is POST /v1/tenants/{id}/workloads/{workload}/promotions.
type PromoteRequest struct {
	TenantID      string `json:"-"`
	Workload      string `json:"-"`
	Preview       string `json:"preview"`
	Host          string `json:"host,omitempty"`
	Replace       bool   `json:"replace,omitempty"`
	ForceTakeover bool   `json:"forceTakeover,omitempty"`
	KeepPreview   bool   `json:"keepPreview,omitempty"`
	By            string `json:"-"`
}

// PromotionResult is the 202 body (Promotion).
type PromotionResult struct {
	Preview        string         `json:"preview"`
	PromotedFrom   string         `json:"promotedFrom"`
	Deployment     deploymentView `json:"deployment"`
	PreviewStopped bool           `json:"previewStopped"`
}

// promoteRequestFor resolves r's preview and returns the deploy that
// promotes it, and the preview's deployment.
func (m *Mgmt) promoteRequestFor(ctx context.Context, r PromoteRequest) (DeployRequest, Deployment, error) {
	if _, err := m.store.GetTenant(ctx, r.TenantID); err != nil {
		return DeployRequest{}, Deployment{}, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if !validName(r.Workload) {
		return DeployRequest{}, Deployment{}, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if r.Preview == "" {
		return DeployRequest{}, Deployment{}, errf(400, "bad_request", "name the preview to promote (its alias; tw previews %s lists them)", r.Workload)
	}
	name := r.Preview
	if !strings.HasPrefix(name, PreviewName(r.Workload, "")) {
		name = PreviewName(r.Workload, name)
	}
	unknown := errf(404, "unknown_preview", "%s has no running preview %q; tw previews %s lists them", r.Workload, r.Preview, r.Workload)
	l, err := m.latestDeployment(ctx, r.TenantID, name)
	if err != nil || l.StoppedAt != 0 || previewOf(l.Namespaces) != r.Workload {
		return DeployRequest{}, Deployment{}, lookupErr(orNotFound(err), unknown)
	}
	d, err := m.store.GetDeployment(ctx, l.ID)
	if err != nil {
		return DeployRequest{}, Deployment{}, err
	}
	if st, e := m.deploymentStatus(ctx, d); st == "failed" {
		return DeployRequest{}, Deployment{}, errf(409, "preview_failed", "preview %s failed on its host (%s); fix it and deploy a new preview", name, firstLineOf(e))
	}
	ns := namespacesIfNoConfig(d)
	for i := range ns {
		ns[i].PreviewOf = ""
	}
	return DeployRequest{TenantID: r.TenantID, Workload: r.Workload, Bundle: d.Bundle, Namespaces: ns,
		Host: r.Host, Replace: r.Replace, ForceTakeover: r.ForceTakeover, ContainerImages: builtFromPinned(r.TenantID, d.Containers),
		By: r.By, preview: &previewCtx{promotedFrom: d.ID, from: d.Workload}}, d, nil
}

// Promote deploys a preview's bundle to its workload through Deploy, then
// undeploys the preview unless KeepPreview.
func (m *Mgmt) Promote(ctx context.Context, r PromoteRequest) (Deployment, DeployResponse, bool, error) {
	dr, pd, err := m.promoteRequestFor(ctx, r)
	if err != nil {
		return pd, DeployResponse{}, false, err
	}
	resp, err := m.Deploy(ctx, dr)
	if err != nil || r.KeepPreview {
		return pd, resp, false, err
	}
	if _, err := m.Undeploy(ctx, pd.ID, "promoted to "+r.Workload, r.By); err != nil {
		m.log.Warn("promote: undeploying the preview failed", "preview", pd.Workload, "err", err)
		return pd, resp, false, nil
	}
	return pd, resp, true, nil
}

// PreflightPromote is the promotion's dry run: the preview's bundle
// against the current policy.
func (m *Mgmt) PreflightPromote(ctx context.Context, r PromoteRequest) (PreflightResult, error) {
	dr, _, err := m.promoteRequestFor(ctx, r)
	if err != nil {
		return PreflightResult{}, err
	}
	return m.PreflightBundle(ctx, dr)
}

// secretsOwnerOf is the workload whose secrets workload runs with: a
// replica's group, a preview's workload, else itself. key names the
// relation for the audit ("" for itself).
func (m *Mgmt) secretsOwnerOf(ctx context.Context, tenantID, workload string) (string, string, error) {
	if g, err := m.replicaGroupOf(ctx, tenantID, workload); err != nil || g != "" {
		return g, "replicaOf", err
	}
	if !strings.Contains(workload, previewSep) {
		return "", "", nil
	}
	d, err := m.latestDeployment(ctx, tenantID, workload)
	if err != nil {
		return "", "", nil
	}
	if of := previewOf(d.Namespaces); of != "" {
		return of, "previewOf", nil
	}
	return "", "", nil
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if l == "" {
		return "no error reported"
	}
	return l
}

// ---- /v1 handlers ----

func (a *v1api) ListWorkloadPreviews(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName) {
	ds, err := a.s.M.previewDeployments(r.Context(), id, workload)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items := make([]Preview, 0, len(ds))
	hs, _ := a.s.M.historyStore()
	for _, d := range ds {
		v, err := a.deploymentView(r, d.ID)
		if err != nil {
			continue
		}
		p := Preview{Workload: d.Workload, Alias: strings.TrimPrefix(d.Workload, PreviewName(workload, "")), PreviewOf: workload, Deployment: v}
		if hs != nil {
			if rec, err := hs.DeploymentRecordByID(r.Context(), d.ID); err == nil {
				p.BundleSHA256 = rec.BundleSHA256
			}
		}
		items = append(items, p)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *v1api) CreatePromotion(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.CreatePromotionParams) {
	var req PromoteRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.TenantID, req.Workload, req.By = id, workload, principalOf(r.Context()).Actor(r)
	if boolOf(params.DryRun) {
		res, err := a.s.M.PreflightPromote(r.Context(), req)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, res)
		return
	}
	pd, resp, stopped, err := a.s.M.Promote(r.Context(), req)
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
	writeJSON(w, 202, PromotionResult{Preview: pd.Workload, PromotedFrom: pd.ID, Deployment: v, PreviewStopped: stopped})
}
