package mgmt

import (
	"context"
	"errors"
	"net/http"
	"strings"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Public-name ownership (ADR-029, red-team H10 / RT3-10).
//
// A workload's public node is named after the workload, so its URL is
// https://<workload>.<tailnet>/. That name is unique per tailnet, not per
// tenant: two tenants on one tailnet that both deploy public routes for
// "foo" race for one hostname, and after a kill and redeploy the later one
// could take over the earlier one's URL (and its webhooks). So mgmt keeps
// one owner per (tailnet, name):
//
//   - the first deploy that declares public routes under a name claims it
//     for its tenant;
//   - the claim survives undeploys, redeploys without "public" and kills;
//     it ends only when the owner releases it (DELETE
//     /v1/tenants/{id}/public-names/{name}, tw expose release) or the
//     tenant is deleted;
//   - a deploy (or dry run) by another tenant on the same tailnet is
//     refused with 409 public_name_owned, and so is a host's create of
//     that tenant's public node.
//
// Scope is the tenant's tailnet (Tenant.Tailnet, lower-cased, no trailing
// dot), which is what the Funnel hostname resolves under; tenants on
// different tailnets never collide.
//
// Names a pre-upgrade deployment already serves have no claim row yet. The
// first claim of such a name goes to the tenant that holds it today (a live
// public node; else the earliest live public deployment), so an upgrade
// never hands a name to whoever deploys first afterwards.

// PublicNameClaim is one owned public name (GET /v1/tenants/{id}/public-names).
type PublicNameClaim struct {
	Tailnet   string `json:"tailnet"`
	Name      string `json:"name"`
	TenantID  string `json:"tenantID"`
	ClaimedAt int64  `json:"claimedAt"`
	ClaimedBy string `json:"claimedBy,omitempty"`
}

// claimedByLegacy marks a claim made for a name a pre-upgrade deployment
// already served.
const claimedByLegacy = "legacy"

// publicNameStore is the store side (sqlstore_publicnames.go).
type publicNameStore interface {
	ClaimPublicName(ctx context.Context, c PublicNameClaim) (PublicNameClaim, bool, error)
	GetPublicName(ctx context.Context, tailnet, name string) (PublicNameClaim, error)
	ListPublicNames(ctx context.Context, tenantID string, pg Page) ([]PublicNameClaim, error)
	ReleasePublicName(ctx context.Context, tailnet, name, tenantID string) error
}

func (m *Mgmt) publicNames() (publicNameStore, error) {
	ps, ok := m.store.(publicNameStore)
	if !ok {
		// Fail closed: without ownership records public deploys are refused.
		return nil, errf(501, "internal", "this mgmt store keeps no public-name ownership")
	}
	return ps, nil
}

// publicTailnet is the ownership scope of t's public names.
func publicTailnet(t Tenant) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(t.Tailnet), "."))
}

func errPublicNameOwned(name, tailnet string) *Error {
	e := errf(409, "public_name_owned",
		"workload %q can't serve public routes: %s.%s is another tenant's public name. tailworks keeps a public name with the first tenant on the tailnet that deployed public routes under it, across undeploys, until that tenant releases it. Deploy under another workload name, or ask the operator to have the owner release it",
		name, name, tailnet)
	e.Missing = map[string]any{"publicName": name}
	return e
}

// publicNameOwner is the tenant that owns name on t's tailnet: the claim
// row, else (claimed=false) the tenant a pre-upgrade deployment serves it
// for ("" if none).
func (m *Mgmt) publicNameOwner(ctx context.Context, ps publicNameStore, t Tenant, name string) (owner string, claimed bool, err error) {
	c, err := ps.GetPublicName(ctx, publicTailnet(t), name)
	if err == nil {
		return c.TenantID, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", false, err
	}
	owner, err = m.legacyPublicOwner(ctx, t, name)
	return owner, false, err
}

// legacyPublicOwner picks, among the tenants on t's tailnet (t included)
// whose latest deployment of name declares public routes, the one that
// holds the hostname now: a live public node first, then the earliest
// deployment, then the tenant ID.
func (m *Mgmt) legacyPublicOwner(ctx context.Context, t Tenant, name string) (string, error) {
	ts, err := m.store.ListTenants(ctx, Page{})
	if err != nil {
		return "", err
	}
	key := publicTailnet(t)
	var best string
	var bestLive bool
	var bestAt int64
	for _, o := range ts {
		if publicTailnet(o) != key {
			continue
		}
		ds, err := m.store.LatestDeployments(ctx, o.ID)
		if err != nil {
			return "", err
		}
		for _, d := range ds {
			if d.Workload != name || d.Public == nil || d.StoppedAt != 0 {
				continue
			}
			pn, perr := m.store.GetObjectByName(ctx, o.ID, name, PublicNamespace, name)
			live := perr == nil && pn.Live()
			better := best == "" ||
				(live && !bestLive) ||
				(live == bestLive && (d.CreatedAt < bestAt || (d.CreatedAt == bestAt && o.ID < best)))
			if better {
				best, bestLive, bestAt = o.ID, live, d.CreatedAt
			}
		}
	}
	return best, nil
}

// checkPublicName refuses (409 public_name_owned) a public deploy of name
// by t when another tenant owns the name. It writes nothing (dry runs).
func (m *Mgmt) checkPublicName(ctx context.Context, t Tenant, name string) error {
	ps, err := m.publicNames()
	if err != nil {
		return err
	}
	owner, _, err := m.publicNameOwner(ctx, ps, t, name)
	if err != nil {
		return asAPIError(err)
	}
	if owner != "" && owner != t.ID {
		return errPublicNameOwned(name, publicTailnet(t))
	}
	return nil
}

// claimPublicName makes t the owner of name on its tailnet, or refuses
// (409 public_name_owned, audited as a deny) when another tenant owns it.
// An unclaimed name a pre-upgrade deployment of another tenant serves is
// claimed for that tenant first.
func (m *Mgmt) claimPublicName(ctx context.Context, t Tenant, name, by string) error {
	ps, err := m.publicNames()
	if err != nil {
		return err
	}
	owner, claimed, err := m.publicNameOwner(ctx, ps, t, name)
	if err != nil {
		return asAPIError(err)
	}
	if !claimed {
		c := PublicNameClaim{Tailnet: publicTailnet(t), Name: name, TenantID: t.ID, ClaimedAt: m.now().UnixMilli(), ClaimedBy: by}
		if owner != "" && owner != t.ID {
			c.TenantID, c.ClaimedBy = owner, claimedByLegacy
		}
		actx, ai := m.intend(ctx, AuditRecord{TenantID: c.TenantID, Kind: "public_name_claim", ActingFor: by},
			map[string]any{"name": name, "tailnet": c.Tailnet, "claimedBy": c.ClaimedBy})
		got, _, err := ps.ClaimPublicName(actx, c)
		if err != nil {
			return asAPIError(err)
		}
		m.recordIntent(ctx, ai, nil) // a no-op unless this call recorded the claim
		owner = got.TenantID
	}
	if owner == t.ID {
		return nil
	}
	m.audit(ctx, AuditRecord{TenantID: t.ID, Kind: "deny", Code: "public_name_owned", ActingFor: by},
		map[string]any{"workload": name, "tailnet": publicTailnet(t), "owner": owner, "message": "public name owned by another tenant"})
	return errPublicNameOwned(name, publicTailnet(t))
}

// ListPublicNames is GET /v1/tenants/{id}/public-names.
func (m *Mgmt) ListPublicNames(ctx context.Context, tenantID string, pg Page) ([]PublicNameClaim, error) {
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return nil, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	ps, err := m.publicNames()
	if err != nil {
		return nil, err
	}
	return ps.ListPublicNames(ctx, tenantID, pg)
}

// ReleasePublicName is DELETE /v1/tenants/{id}/public-names/{name}: the
// owner gives a name up, once nothing serves it.
func (m *Mgmt) ReleasePublicName(ctx context.Context, tenantID, name, by string) error {
	t, err := m.store.GetTenant(ctx, tenantID)
	if err != nil {
		return lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	ps, err := m.publicNames()
	if err != nil {
		return err
	}
	key := publicTailnet(t)
	c, err := ps.GetPublicName(ctx, key, name)
	if errors.Is(err, ErrNotFound) || (err == nil && c.TenantID != t.ID) {
		return errf(404, "not_found", "tenant %s owns no public name %q", t.ID, name)
	}
	if err != nil {
		return asAPIError(err)
	}
	ds, err := m.store.LatestDeployments(ctx, t.ID)
	if err != nil {
		return asAPIError(err)
	}
	// A team tenant's public name is its workload's name with the tenant's
	// prefix (t.Name, the claim in public.go); check the workload under
	// both spellings, so a prefixed name can't be released while served.
	workload := name
	if t.NamePrefix != "" {
		workload = strings.TrimPrefix(name, t.NamePrefix+"-")
	}
	inUse := false
	for _, d := range ds {
		inUse = inUse || ((d.Workload == name || d.Workload == workload) && d.Public != nil && d.StoppedAt == 0)
	}
	for _, w := range []string{name, workload} {
		if o, err := m.store.GetObjectByName(ctx, t.ID, w, PublicNamespace, w); err == nil && o.Live() {
			inUse = true
		}
	}
	if inUse {
		return errf(409, "public_name_in_use",
			"workload %q still serves public routes as %s.%s; undeploy it (tw undeploy %s), or remove \"public\" from its tailworks.jsonc and redeploy, then release the name (tw expose release %s)",
			workload, name, key, workload, name)
	}
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, Kind: "public_name_release", ActingFor: by},
		map[string]any{"name": name, "tailnet": key, "by": by})
	if err := ps.ReleasePublicName(actx, key, name, t.ID); err != nil {
		return lookupErr(err, errf(404, "not_found", "tenant %s owns no public name %q", t.ID, name))
	}
	m.recordIntent(ctx, ai, nil)
	return nil
}

func (a *v1api) ListPublicNames(w http.ResponseWriter, r *http.Request, id v1.TenantID, params v1.ListPublicNamesParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rows, err := a.s.M.ListPublicNames(r.Context(), id, pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, mkPage(rows, n, func(c PublicNameClaim) PageKey { return PageKey{Created: c.ClaimedAt, ID: c.Name} }))
}

func (a *v1api) ReleasePublicName(w http.ResponseWriter, r *http.Request, id v1.TenantID, name v1.PublicName) {
	if err := a.s.M.ReleasePublicName(r.Context(), id, name, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}
