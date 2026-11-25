package mgmt

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A host gets every tenant's credential file paths, including tenants it
// runs nothing of (GET /v1/tenants hides those from it), and nothing that
// names a tenant; it may read only its own host ID.
func TestHostSandboxDeny(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	// A third tenant with its own key file, deployed nowhere.
	other := writeTestOAuthFile(t, filepath.Join(t.TempDir(), "other-oauth.env"))
	var tn3 struct{ TenantID string }
	a.c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "third.ts.net", "mintMode": "oauth", "oauthClientFile": other}, &tn3)

	var got struct{ Paths []string }
	if st, eb := a.call(nodeH1, "GET", "/v1/hosts/h1/sandbox-deny", nil, &got); st != 200 {
		t.Fatalf("h1: %d %v", st, eb)
	}
	if !slices.Contains(got.Paths, a.keyFile) || !slices.Contains(got.Paths, other) {
		t.Fatalf("paths %v: want %s and the undeployed tenant's %s", got.Paths, a.keyFile, other)
	}
	if !slices.IsSorted(got.Paths) || len(slices.Compact(slices.Clone(got.Paths))) != len(got.Paths) {
		t.Fatalf("paths not sorted and unique: %v", got.Paths)
	}
	// Host h1 doesn't see the third tenant in the tenant list (M3)…
	var page struct{ Items []struct{ ID string } }
	a.call(nodeH1, "GET", "/v1/tenants", nil, &page)
	for _, x := range page.Items {
		if x.ID == tn3.TenantID {
			t.Fatal("GET /v1/tenants showed host h1 a tenant it runs nothing of")
		}
	}
	// …and the deny list names no tenant.
	for _, p := range got.Paths {
		for _, id := range []string{a.tn, a.tn2, tn3.TenantID, "third.ts.net", "other.ts.net"} {
			if strings.Contains(p, id) {
				t.Fatalf("path %q names tenant %s", p, id)
			}
		}
	}
	a.fail(nodeH1, "GET", "/v1/hosts/h2/sandbox-deny", nil, 403, "forbidden")
}
