package mgmt

import (
	"fmt"
	"testing"
)

// TestServiceBindingTarget: GET .../service-bindings/{binding} (ADR-053)
// answers the lease holder of the calling workload with the target's entry
// node, only for a binding its latest deployment declares, and only for a
// workload of the same tenant.
func TestServiceBindingTarget(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"},
		"version": "0.2.0", "features": []string{"service-bindings"}}, nil)
	tw := `{ "tailnet": { "tags": ["tag:tw-wl"] } }`
	deploy := func(workload, wrangler string) int64 {
		b := makeBundle(t, map[string]string{"tailworks.jsonc": tw, "wrangler.jsonc": wrangler, "dist/index.js": "export default {}"})
		var dep DeployResponse
		c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": workload, "bundle": b}, &dep)
		return dep.Epoch
	}
	shopEpoch := deploy("shop", `{ "name": "shop", "main": "dist/index.js", "services": [
		{ "binding": "AUTH", "service": "auth", "entrypoint": "Admin" },
		{ "binding": "GHOST", "service": "nosuch" } ] }`)
	path := func(binding string, epoch int64) string {
		return fmt.Sprintf("/v1/tenants/%s/workloads/shop/service-bindings/%s?epoch=%d", tn.TenantID, binding, epoch)
	}

	h1.fail("GET", path("AUTH", shopEpoch), nil, 404, "service_not_found") // auth was never deployed
	h1.fail("GET", path("GHOST", shopEpoch), nil, 404, "service_not_found")
	h1.fail("GET", path("NOPE", shopEpoch), nil, 404, "service_not_found") // not declared
	h1.fail("GET", path("AUTH", shopEpoch+1), nil, 409, "lease_lost")

	authEpoch := deploy("auth", `{ "name": "auth", "main": "dist/index.js" }`)
	h1.fail("GET", path("AUTH", shopEpoch), nil, 503, "identity_pending") // deployed, no entry node yet
	var obj CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "auth", "namespace": "_workload", "objectName": "auth", "epoch": authEpoch}, &obj)
	h1.fail("GET", path("AUTH", shopEpoch), nil, 503, "identity_pending") // no MagicDNS name yet
	h1.ok("POST", "/v1/objects/"+obj.ObjectID+fmt.Sprintf("/credentials?epoch=%d", authEpoch), nil, &Credential{})
	h1.ok("PATCH", "/v1/objects/"+obj.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": "nA", "magicDNS": obj.ObjectID + ".t.ts.net.", "epoch": authEpoch}, nil)
	var got ServiceBindingTarget
	h1.ok("GET", path("AUTH", shopEpoch), nil, &got)
	if got.Service != "auth" || got.Entrypoint != "Admin" || got.ObjectID != obj.ObjectID || got.MagicDNS != obj.ObjectID+".t.ts.net" || got.Status != "ready" {
		t.Fatalf("target: %+v", got)
	}

	// Another tenant's workload named auth is not a target: the lookup is
	// in the caller's tenant only.
	var tn2 struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "u.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn2)
	b := makeBundle(t, map[string]string{"tailworks.jsonc": tw, "wrangler.jsonc": `{ "name": "shop", "main": "dist/index.js", "services": [{ "binding": "AUTH", "service": "auth" }] }`, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn2.TenantID, "workload": "shop", "bundle": b}, &dep)
	h1.fail("GET", fmt.Sprintf("/v1/tenants/%s/workloads/shop/service-bindings/AUTH?epoch=%d", tn2.TenantID, dep.Epoch), nil, 404, "service_not_found")
}
