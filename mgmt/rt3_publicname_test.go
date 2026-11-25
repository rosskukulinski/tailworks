package mgmt

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Red-team H10 / RT3-10: a public workload is https://<name>.<tailnet>/, so
// two tenants on one tailnet that deploy public routes under the same name
// race for one hostname, and after a kill and redeploy the later one took
// over the earlier one's URL (and its webhooks). mgmt now keeps one owner
// per (tailnet, name), from the first public deploy until the owner
// releases it (publicnames.go).

func publicNameClaims(t *testing.T, e *testEnv, tenant string) []PublicNameClaim {
	t.Helper()
	var out []PublicNameClaim
	e.c.items("/v1/tenants/"+tenant+"/public-names", &out)
	return out
}

func TestRT3_H10_PublicNameSquat(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	publicHost(t, e)
	allow := map[string]any{"publicIngress": "allow"}
	a := v1Tenant(t, e, "t.ts.net", allow)
	b := legacySharedTenant(t, e, a, "t.ts.net")
	other := v1Tenant(t, e, "other.ts.net", allow)
	pub := publicBundle(t, publicTwJSONC)
	deploy := func(tn string) map[string]any {
		return map[string]any{"tenantID": tn, "workload": "support", "bundle": pub}
	}

	// A deploys public "support" first and gets its public node.
	var depA DeployResponse
	c.ok("POST", "/v1/deployments", deploy(a), &depA)
	var pn CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": a, "workload": "support", "namespace": PublicNamespace, "objectName": "support"}, &pn)

	// B, on the same tailnet, can't take the name: not by deploy, not by dry run.
	eb := c.fail("POST", "/v1/deployments", deploy(b), 409, "public_name_owned")
	if cl := publicNameClaims(t, e, a); len(cl) != 1 || cl[0].Name != "support" || cl[0].Tailnet != "t.ts.net" || cl[0].TenantID != a {
		t.Fatalf("A's claims %+v", cl)
	}
	if msg, _ := eb["message"].(string); !strings.Contains(msg, "support.t.ts.net") || strings.Contains(msg, a) {
		t.Fatalf("refusal names no hostname, or leaks the owner: %v", eb)
	}
	c.fail("POST", "/v1/deployments?dryRun=true", deploy(b), 409, "public_name_owned")
	if recs := auditKinds(t, e, b, "deny"); len(recs) == 0 || recs[len(recs)-1].Code != "public_name_owned" ||
		!strings.Contains(string(recs[len(recs)-1].Detail), `"owner":"`+a+`"`) {
		t.Fatalf("B's deny audit: %+v", recs)
	}
	// Spelling the tailnet differently doesn't escape the scope.
	b2 := legacySharedTenant(t, e, a, "T.ts.net.")
	c.fail("POST", "/v1/deployments", deploy(b2), 409, "public_name_owned")
	// A tailnet-only deploy of the same name is unaffected (no public node).
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": b, "workload": "support", "bundle": publicBundle(t, tailnetOnlyTwJSONC)}, nil)
	// ...and so is a tenant on another tailnet.
	c.ok("POST", "/v1/deployments", deploy(other), nil)

	// The attack: A's public node dies (kill, then A undeploys). The name
	// stays A's, so B still can't claim it and receive A's webhooks.
	c.ok("DELETE", "/v1/objects/"+pn.ObjectID, nil, nil)
	c.ok("DELETE", "/v1/deployments/"+depA.DeploymentID, nil, nil)
	time.Sleep(3 * time.Millisecond)
	c.fail("POST", "/v1/deployments", deploy(b), 409, "public_name_owned")
	// B's host can't mint the public node either (B has no public deployment).
	h1.fail("POST", "/v1/objects", map[string]any{"tenantID": b, "workload": "support", "namespace": PublicNamespace, "objectName": "support"}, 404, "unknown_namespace")

	// A redeploys: still its name.
	time.Sleep(3 * time.Millisecond)
	var depA2 DeployResponse
	c.ok("POST", "/v1/deployments", deploy(a), &depA2)

	// Release: refused while served, then allowed; B can claim it afterwards.
	c.fail("DELETE", "/v1/tenants/"+b+"/public-names/support", nil, 404, "not_found")
	c.fail("DELETE", "/v1/tenants/"+a+"/public-names/support", nil, 409, "public_name_in_use")
	c.ok("DELETE", "/v1/deployments/"+depA2.DeploymentID, nil, nil)
	if st := c.okStatus("DELETE", "/v1/tenants/"+a+"/public-names/support", nil, nil); st != 204 {
		t.Fatalf("release: %d", st)
	}
	if recs := auditKinds(t, e, a, "public_name_claim", "public_name_release"); len(recs) != 2 || recs[0].Kind != "public_name_claim" || recs[1].Kind != "public_name_release" {
		t.Fatalf("A's claim/release audit: %+v", recs)
	}
	time.Sleep(3 * time.Millisecond)
	c.ok("POST", "/v1/deployments", deploy(b), nil)
	if cl := publicNameClaims(t, e, b); len(cl) != 1 || cl[0].TenantID != b {
		t.Fatalf("B's claims %+v", cl)
	}
	if cl := publicNameClaims(t, e, a); len(cl) != 0 {
		t.Fatalf("A still claims %+v", cl)
	}
	time.Sleep(3 * time.Millisecond)
	c.fail("POST", "/v1/deployments", deploy(a), 409, "public_name_owned")

	// Deleting the owner tenant releases its names.
	c.ok("DELETE", "/v1/tenants/"+b+"?force=true", nil, nil)
	time.Sleep(3 * time.Millisecond)
	c.ok("POST", "/v1/deployments", deploy(a), nil)
}

// A team tenant's public name carries its prefix (t.Name), and releasing
// it is refused while the workload behind it still serves public routes.
func TestRT3_H10_PrefixedPublicNameRelease(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	publicHost(t, e)
	tn := v1Tenant(t, e, "t.ts.net", map[string]any{"publicIngress": "allow", "ownerTag": "tag:tw-t-pay", "namePrefix": "pay"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "support", "bundle": publicBundle(t, publicTwJSONC)}, &dep)
	if cl := publicNameClaims(t, e, tn); len(cl) != 1 || cl[0].Name != "pay-support" {
		t.Fatalf("claims %+v", cl)
	}
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn, "workload": "support", "namespace": PublicNamespace, "objectName": "support"}, nil)
	eb := c.fail("DELETE", "/v1/tenants/"+tn+"/public-names/pay-support", nil, 409, "public_name_in_use")
	if msg, _ := eb["message"].(string); !strings.Contains(msg, "tw undeploy support") || !strings.Contains(msg, "tw expose release pay-support") {
		t.Fatalf("in-use message: %v", eb)
	}
	c.ok("DELETE", "/v1/deployments/"+dep.DeploymentID, nil, nil)
	if st := c.okStatus("DELETE", "/v1/tenants/"+tn+"/public-names/pay-support", nil, nil); st != 204 {
		t.Fatalf("release: %d", st)
	}
}

// A name a pre-upgrade deployment already serves has no claim row: the
// first claim goes to the tenant serving it, not to whoever deploys next.
func TestRT3_H10_PublicNameLegacyOwner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	publicHost(t, e)
	allow := map[string]any{"publicIngress": "allow"}
	a := v1Tenant(t, e, "t.ts.net", allow)
	b := legacySharedTenant(t, e, a, "t.ts.net")
	pub := publicBundle(t, publicTwJSONC)
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a, "workload": "support", "bundle": pub}, nil)
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": a, "workload": "support", "namespace": PublicNamespace, "objectName": "support"}, nil)
	// Before this fix there was no claim table: drop A's row.
	if _, err := e.store.db.ExecContext(context.Background(), "DELETE FROM public_names"); err != nil {
		t.Fatal(err)
	}
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": b, "workload": "support", "bundle": pub}, 409, "public_name_owned")
	if cl := publicNameClaims(t, e, a); len(cl) != 1 || cl[0].ClaimedBy != claimedByLegacy {
		t.Fatalf("A's backfilled claim %+v", cl)
	}
}

// legacySharedTenant is a second tenant on tenant of's tailnet with neither
// an owner tag nor a name prefix, as installs from before ADR-043
// can have. The API now refuses to create one (shared_owner_tag), so it
// goes straight into the store.
func legacySharedTenant(t *testing.T, e *testEnv, of, tailnet string) string {
	t.Helper()
	ctx := context.Background()
	x, err := e.store.GetTenant(ctx, of)
	if err != nil {
		t.Fatal(err)
	}
	x.ID, x.Tailnet = newID("tn_")[:11], tailnet
	if err := e.store.CreateTenant(ctx, x); err != nil {
		t.Fatal(err)
	}
	return x.ID
}
