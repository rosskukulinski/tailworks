package main

import "strings"

// The admin console URL that `tw link` and `tw whoami` print (issue #478),
// so a linked developer, the console's audience, finds it without `tw up`'s
// output. It is built from the linked endpoint, not mgmt's DNS name, so a
// link to a Tailscale Service name keeps that name.

// consoleURL is the console page the caller may open at a linked endpoint,
// or "" when there is none: under --tsnet (the CLI's own node is the only
// tailnet path, so this machine's browser can't reach mgmt), and when mgmt's
// mountUI would refuse the caller. An approver who is neither an admin nor a
// tenant admin gets the approvals page only.
func consoleURL(l *Link, admin, tenantAdmin, approver bool) string {
	if l == nil || l.Tsnet != nil || !strings.Contains(l.Endpoint, "://") {
		return ""
	}
	ep := strings.TrimSuffix(l.Endpoint, "/")
	switch {
	case admin || tenantAdmin:
		return ep + "/ui/"
	case approver:
		return ep + "/ui/approvals"
	}
	return ""
}
