package mgmt

import (
	"fmt"
	"strings"
	"testing"
)

// TestPoolNodeUndeclaredClass (#359): a "_pool" node of a class the
// deployment doesn't declare is refused (unknown_namespace), and the refusal
// names that class, not "_pool" (tw-hostd logged `declares namespace
// "_pool"` once a second, which pointed at the wrong thing).
func TestPoolNodeUndeclaredClass(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, nil)

	r := h1.fail("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": PoolNamespace,
		"objectName": "TaskAgent/p1"}, 404, "unknown_namespace")
	msg := fmt.Sprint(r["message"])
	if !strings.Contains(msg, `"TaskAgent"`) || strings.Contains(msg, `namespace "_pool"`) {
		t.Fatalf("refusal of a pool node of an undeclared class: %q (want it to name TaskAgent)", msg)
	}
	// The class itself: the same refusal, naming it.
	r = h1.fail("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "TaskAgent",
		"objectName": "a1"}, 404, "unknown_namespace")
	if msg := fmt.Sprint(r["message"]); !strings.Contains(msg, `"TaskAgent"`) {
		t.Fatalf("refusal of an undeclared class: %q", msg)
	}
}
