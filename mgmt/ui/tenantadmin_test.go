package ui

import (
	"strings"
	"testing"
)

// TestConsoleAdmitsTenantAdmins: a caller holding only a tenant-admin
// grant gets the console for its tenants, without the hosts panel; a
// caller with no grant at all gets 403 forbidden (docs/ADMIN-UI.md's
// table, which once said tenant admins were refused).
func TestConsoleAdmitsTenantAdmins(t *testing.T) {
	who := func(grants []any) map[string]any {
		return map[string]any{"kind": "user", "login": "t@x", "admin": false, "actor": "t@x",
			"tenantGrants": grants,
			"mgmt":         map[string]any{"dnsName": "tw-mgmt.t.ts.net", "tailnet": "t.ts.net"}}
	}

	f := populatedAPI()
	f.routes["GET /v1/whoami"] = who([]any{map[string]any{"tenants": []string{"tn_1"}, "actions": []string{"read", "deploy"}}})
	h := serve(t, f)
	w := get(t, h, "/ui/")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "tenant admin") || strings.Contains(body, `id="hosts"`) {
		t.Fatalf("tenant admin: %d (want 200 with the tenant-admin note and no hosts panel)\n%s", w.Code, body)
	}

	f = populatedAPI()
	f.routes["GET /v1/whoami"] = who(nil)
	h = serve(t, f)
	if w := get(t, h, "/ui/"); w.Code != 403 || !strings.Contains(w.Body.String(), "forbidden") {
		t.Fatalf("no grant: %d (want 403 forbidden)\n%s", w.Code, w.Body.String())
	}
}
