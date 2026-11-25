package mgmt

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
)

// GET /v1/whoami's scope (issue #50): an approver who is not a tailworks admin
// cannot list tenants, so it never learned the tenant ID the approvals
// routes need. whoami now names the caller's own tailnet (from WhoIs) and
// the tenants on it, plus the caller's github.com/rosskukulinski/tailworks/cap/approve grants, and the
// admin UI and `tw approvals` default to those tenants.

// WhoAmITenant is a tenant the caller can act in.
type WhoAmITenant struct {
	TenantID string `json:"tenantID"`
	Tailnet  string `json:"tailnet"`
}

// ApproveGrant is one github.com/rosskukulinski/tailworks/cap/approve grant value
// (approvecap.Grant; a missing field does not narrow).
type ApproveGrant struct {
	Workloads []string           `json:"workloads,omitempty"`
	Workflows []string           `json:"workflows,omitempty"`
	Steps     []string           `json:"steps,omitempty"`
	Decisions []string           `json:"decisions,omitempty"`
	Limits    map[string]float64 `json:"limits,omitempty"`
}

// whoamiMaxTenants bounds the tenants whoami lists.
const whoamiMaxTenants = 500

// tailnetOfName is the MagicDNS suffix of a node name
// ("laptop.tail1234.ts.net." → "tail1234.ts.net"); "" for a bare name.
func tailnetOfName(name string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	_, suffix, ok := strings.Cut(name, ".")
	if !ok {
		return ""
	}
	return suffix
}

func normTailnet(t string) string { return strings.TrimSuffix(strings.ToLower(t), ".") }

// whoamiScope fills who's tailnet, tenants and approve grants.
func (s *Server) whoamiScope(ctx context.Context, p Principal, who *WhoAmI) {
	who.Tenants, who.ApproveGrants = []WhoAmITenant{}, []ApproveGrant{}
	who.TenantGrants, who.InvalidTenantGrants = whoamiTenantGrants(p) // tenantscope.go
	for _, raw := range p.CapValues[approvecap.Cap] {
		var g ApproveGrant
		if json.Unmarshal(raw, &g) == nil {
			who.ApproveGrants = append(who.ApproveGrants, g)
		}
	}
	switch p.Kind {
	case "host", "":
		return
	case "user", "mcp":
		who.Tailnet = tailnetOfName(p.Name)
		if who.Tailnet == "" {
			who.Tailnet = normTailnet(s.Self.Tailnet)
		}
		if who.Tailnet == "" {
			return
		}
	}
	ts, err := s.M.store.ListTenants(ctx, Page{Limit: whoamiMaxTenants})
	if err != nil {
		return
	}
	onlyGranted := (s.tenantScoped(p) || tenantApprovers(p)) && !s.allowed(p, roleAdmin) && !plainApprover(p) // tenantscope.go
	for _, t := range ts {
		if onlyGranted && !slices.ContainsFunc(p.tenantGrants(), func(g TenantGrant) bool { return slices.Contains(g.Tenants, t.ID) }) {
			continue
		}
		if p.Kind == "local" || normTailnet(t.Tailnet) == who.Tailnet {
			who.Tenants = append(who.Tenants, WhoAmITenant{TenantID: t.ID, Tailnet: t.Tailnet})
		}
	}
}
