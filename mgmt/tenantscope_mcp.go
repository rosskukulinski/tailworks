package mgmt

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
)

// Tenant admins on tailworks' MCP server (ADR-043 D2). A TenantAdminCap
// value with an "mcp" entry admits the caller to /mcp for that value's
// tenants:
//
//	{"tenants": ["payments"], "actions": ["read", "kill"], "mcp": {"tools": ["kill"]}}
//
// gives whoami, ls, logs and audit (with "read") and kill (named, and
// "kill" in the same value's actions). Unlike a github.com/rosskukulinski/tailworks/cap/mcp holder,
// whose tools run as an admin (ADR-035), these tools run as the caller,
// limited to the values that carry "mcp": the /v1 tenant gate decides
// every call they make.

// tenantMCPAction is the action a tool needs from a tenant admin ("" =
// none: whoami). status is an operator tool and is never offered.
var tenantMCPAction = map[string]string{
	"audit":    ActionRead,
	"deploy":   ActionDeploy,
	"kill":     ActionKill,
	"logs":     ActionRead,
	"ls":       ActionRead,
	"undeploy": ActionDeploy,
	"whoami":   "",
}

// badSubValues checks a value's "mcp" and "approve" entries ("" = fine).
func badSubValues(g TenantGrant) string {
	if g.MCP != nil {
		for _, t := range g.MCP.Tools {
			if _, ok := tenantMCPAction[t]; !ok && t != "*" {
				return fmt.Sprintf("mcp names %q, which isn't a tool tenant admins can have", t)
			}
		}
	}
	for _, raw := range g.Approve {
		var a approvecap.Grant
		if json.Unmarshal(raw, &a) != nil {
			return "approve entries must be github.com/rosskukulinski/tailworks/cap/approve values (objects)"
		}
	}
	return ""
}

// tenantMCPTools are the tools p's TenantAdminCap values give it on /mcp.
func tenantMCPTools(p Principal) map[string]bool {
	out := map[string]bool{}
	for _, g := range p.tenantGrants() {
		if g.MCP == nil {
			continue
		}
		for _, t := range mcpTools {
			action, ok := tenantMCPAction[t.Name]
			switch {
			case !ok:
			case t.Kind == "read" && (action == "" || slices.Contains(g.Actions, action)):
				out[t.Name] = true
			case t.Kind != "read" && (slices.Contains(g.MCP.Tools, t.Name) || slices.Contains(g.MCP.Tools, "*")) && slices.Contains(g.Actions, action):
				out[t.Name] = true
			}
		}
	}
	return out
}

// tenantMCPPrincipal is p as its tenant-admin tools run: only the values
// that carry "mcp", so a tool reaches no tenant that wasn't granted for MCP.
func tenantMCPPrincipal(p Principal) Principal {
	var keep []json.RawMessage
	for _, raw := range p.CapValues[TenantAdminCap] {
		if g, _ := parseTenantGrants([]json.RawMessage{raw}); len(g) == 1 && g[0].MCP != nil {
			keep = append(keep, raw)
		}
	}
	vals := make(map[string][]json.RawMessage, len(p.CapValues))
	for k, v := range p.CapValues {
		vals[k] = v
	}
	vals[TenantAdminCap] = keep
	p.CapValues = vals
	return p
}

// tenantMCPNotGranted is the refusal for a tenant admin, with the value
// that would add the tool.
func tenantMCPNotGranted(p Principal, t mcpTool) *Error {
	src := p.Login
	if len(p.Tags) > 0 {
		src = p.Tags[0]
	}
	action := tenantMCPAction[t.Name]
	if action == "" {
		action = ActionRead
	}
	e := errf(403, "cap_missing", "%s may not call the %s tool: no %s value with \"mcp\" names it with the %q action", p.Login, t.Name, TenantAdminCap, action)
	e.Snippet = `{"src": ["` + src + `"], "dst": ["` + MgmtTag + `"], "ip": ["tcp:443", "tcp:80"], "app": {"` + TenantAdminCap +
		`": [{"tenants": ["<tenant>"], "actions": ["` + action + `"], "mcp": {"tools": ["` + t.Name + `"]}}]}}`
	e.Missing = map[string]any{"src": src, "dst": MgmtTag, "cap": TenantAdminCap, "tool": t.Name}
	return e
}
