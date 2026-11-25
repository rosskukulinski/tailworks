package mgmt

import (
	"strings"
	"testing"
)

// TestDeployRefusesExperimentalFlags (#337, ledger H-compat-flags): a
// bundle whose compatibility_flags list experimental flags is refused with
// the typed experimental_flag error at the deploy and at its dry run (tw
// deploy's preflight) while no host allows them; once a host advertises
// experimental-flags (-allow-experimental-flags), the deploy is placed
// there. Stable flags are unaffected.
func TestDeployRefusesExperimentalFlags(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	c := e.c
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1 := e.asHost("h1")
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	mk := func(flags string) []byte {
		return makeBundle(t, map[string]string{
			"wrangler.jsonc":  `{"name":"web","main":"index.js","compatibility_date":"2026-04-01","compatibility_flags":[` + flags + `]}`,
			"index.js":        "export default {}",
			"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"] } }`,
		})
	}
	exp := mk(`"nodejs_compat","unsafe_module","rtti_api"`)
	body := func(workload string, b []byte) map[string]any {
		return map[string]any{"tenantID": tn.TenantID, "workload": workload, "bundle": b}
	}

	// Default policy: refused at the dry run and at the deploy, naming the
	// flags and the switch; nothing is deployed.
	for _, path := range []string{"/v1/deployments?dryRun=true", "/v1/deployments"} {
		er := c.fail("POST", path, body("support", exp), 403, "experimental_flag")
		if msg, _ := er["message"].(string); !strings.Contains(msg, "rtti_api, unsafe_module") || !strings.Contains(msg, "-allow-experimental-flags") {
			t.Fatalf("%s: message %q", path, msg)
		}
	}
	var as []Assignment
	if h1.items("/v1/hosts/h1/assignments", &as); len(as) != 0 {
		t.Fatalf("assignments after refusals: %+v", as)
	}

	// Stable flags deploy as before.
	c.ok("POST", "/v1/deployments?dryRun=true", body("support", mk(`"nodejs_compat"`)), nil)
	c.ok("POST", "/v1/deployments", body("support", mk(`"nodejs_compat"`)), nil)

	// A host that allows them (-allow-experimental-flags advertises the
	// feature): the deploy passes and is placed there, not on h1.
	h2 := e.asHost("h2")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}, "features": []string{"experimental-flags"}}, nil)
	c.ok("POST", "/v1/deployments?dryRun=true", body("unsafe", exp), nil)
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", body("unsafe", exp), &dep)
	if dep.Host != "h2" {
		t.Fatalf("deploy with experimental flags placed on %q, want h2 (the host that allows them): %+v", dep.Host, dep)
	}
}
