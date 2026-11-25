package mgmt

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
)

// Access warnings (ADR-043 D2): the deploy preflight, which
// already reads the tenant's tailnet policy, also reports grants to
// tag:tw-mgmt that don't do what they look like they do. `tw policy` and
// `tw deploy` print them; they never fail a deploy.

// accessWarnings inspects pol's grants to mgmt for tenant t.
func (m *Mgmt) accessWarnings(ctx context.Context, t Tenant, pol *Policy) []string {
	if pol == nil {
		return nil
	}
	known := map[string]bool{}
	var others []string // other tenants on t's tailnet
	if ts, err := m.store.ListTenants(ctx, Page{}); err == nil {
		for _, x := range ts {
			known[x.ID] = true
			if x.ID != t.ID && normTailnet(x.Tailnet) == normTailnet(t.Tailnet) {
				others = append(others, x.ID)
			}
		}
	}
	slices.Sort(others)
	var out []string
	add := func(format string, a ...any) {
		if w := fmt.Sprintf(format, a...); !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	for _, g := range pol.Grants {
		if !slices.Contains(g.Dst, MgmtTag) && !slices.Contains(g.Dst, "*") {
			continue
		}
		src := strings.Join(g.Src, ", ")
		for _, raw := range g.App[AdminCap] {
			if v := strings.TrimSpace(string(raw)); v != "{}" && v != "null" {
				add("%s → %s: values on %s don't narrow it (%s): %s is a full admin of every tenant and host; use %s to scope a team", src, MgmtTag, AdminCap, v, src, TenantAdminCap)
			}
		}
		grants, bad := parseTenantGrants(g.App[TenantAdminCap])
		for _, why := range bad {
			add("%s → %s: a %s value grants nothing: %s", src, MgmtTag, TenantAdminCap, why)
		}
		for _, tg := range grants {
			for _, id := range tg.Tenants {
				if !known[id] {
					add("%s → %s: %s names %q, which is not a tenant on this tailworks (tenant IDs: tw tenant ls)", src, MgmtTag, TenantAdminCap, id)
				}
			}
		}
		if len(others) == 0 {
			continue
		}
		for _, c := range []string{MCPCap, approvecap.Cap} {
			if _, ok := g.App[c]; ok {
				add("%s → %s: %s applies to every tenant on this tailnet (%s too); to limit it to one team, use %s with an %q entry",
					src, MgmtTag, c, strings.Join(others, ", "), TenantAdminCap, map[string]string{MCPCap: "mcp", approvecap.Cap: "approve"}[c])
			}
		}
	}
	return out
}
