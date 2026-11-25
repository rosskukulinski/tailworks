package mgmt

import (
	"encoding/json"
	"slices"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
)

// Tenant-scoped approvers (ADR-043 D2). A TenantAdminCap value
// may carry "approve": [<github.com/rosskukulinski/tailworks/cap/approve value>, ...]. Those apply
// only to approvals in the value's tenants. mgmt sends the host just the
// values that apply to the tenant being decided, so the host's check
// (internal/approvecap) needs no change, and a host from before this file
// still sees only what applies.
//
// "actions" don't make anyone an approver: the "approve" entry is the
// grant, as github.com/rosskukulinski/tailworks/cap/approve is (ADR-040).

// tenantApprovers: p holds an "approve" entry in some tenant-admin value.
func tenantApprovers(p Principal) bool {
	return slices.ContainsFunc(p.tenantGrants(), func(g TenantGrant) bool { return len(g.Approve) > 0 })
}

// plainApprover: p holds github.com/rosskukulinski/tailworks/cap/approve itself (every tenant).
func plainApprover(p Principal) bool {
	return slices.Contains(p.Caps, approvecap.Cap)
}

// approveGrantsFor are the approve values that apply to tenant: the
// caller's github.com/rosskukulinski/tailworks/cap/approve values, plus its tenant-admin "approve"
// entries from values naming tenant.
func approveGrantsFor(p Principal, tenant string) []json.RawMessage {
	out := slices.Clone(p.CapValues[approvecap.Cap])
	for _, g := range p.tenantGrants() {
		if slices.Contains(g.Tenants, tenant) {
			out = append(out, g.Approve...)
		}
	}
	if out == nil {
		out = []json.RawMessage{}
	}
	return out
}

// tenantApproveOK refuses a caller whose only approver power is
// tenant-scoped and doesn't cover tenant (nil for admins, the local
// operator, and holders of github.com/rosskukulinski/tailworks/cap/approve).
func (s *Server) tenantApproveOK(p Principal, tenant string) error {
	if p.Kind != "user" || s.allowed(p, roleAdmin) || plainApprover(p) {
		return nil
	}
	for _, g := range p.tenantGrants() {
		if len(g.Approve) > 0 && slices.Contains(g.Tenants, tenant) {
			return nil
		}
	}
	e := tenantForbidden(p, tenant, "approve")
	e.Snippet = `{"src": ["` + p.Login + `"], "dst": ["` + MgmtTag + `"], "ip": ["tcp:443", "tcp:80"], "app": {"` + TenantAdminCap +
		`": [{"tenants": ["` + tenant + `"], "approve": [{}]}]}}`
	return e
}
