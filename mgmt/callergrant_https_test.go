package mgmt

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// TestPolicyApplyCallerGrantHTTPS: an app with tailnet.https gets a caller
// grant on tcp:443 as well as tcp:80 (its node serves both, #360); an app
// without it keeps tcp:80 only (TestPolicyApplyCallerGrant).
func TestPolicyApplyCallerGrantHTTPS(t *testing.T) {
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	grantPolicyWrite(e)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	body := map[string]any{"bundle": makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"name": "app", "tailnet": {"tags": ["tag:tw-wl-app"], "https": true}}`,
		"dist/index.js":   "export default {}",
	})}
	const grant = `{ "src": ["autogroup:member"], "dst": ["tag:tw-wl-app"], "ip": ["tcp:80", "tcp:443"] }`
	var res PolicyApplyResult
	e.c.ok("POST", "/v1/tenants/"+tn+"/workloads/app/policy", body, &res)
	if !res.Changed || res.CallerGrant != grant {
		t.Fatalf("apply %+v", res)
	}
	after, _ := e.policyState()
	added := newAppSnippet(appTag)
	added.Grants = []map[string]any{{"src": []string{"autogroup:member"}, "dst": []string{appTag}, "ip": []string{"tcp:80", "tcp:443"}}}
	if err := policy.VerifyAdditive([]byte(operatorOnly), []byte(after), added); err != nil {
		t.Fatalf("not exactly the tag and the caller grant on both ports: %v\n%s", err, after)
	}
	if !strings.Contains(after, "tcp:443") {
		t.Fatalf("tcp:443 not written:\n%s", after)
	}
}

func TestCallerPorts(t *testing.T) {
	if got := strings.Join(CallerPorts(false), ","); got != "tcp:80" {
		t.Errorf("without https: %s", got)
	}
	if got := strings.Join(CallerPorts(true), ","); got != "tcp:80,tcp:443" {
		t.Errorf("with https: %s", got)
	}
}
