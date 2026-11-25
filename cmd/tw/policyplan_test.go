package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCLIPolicyPlan: tw policy prints one snippet with everything missing
// from the live policy plus the caller grant, exits acl_denied while a
// checked part is missing, and deploys nothing (issue #73).
func TestCLIPolicyPlan(t *testing.T) {
	e := newPubEnv(t) // public_test.go: a real tw-mgmt and a fake policy API
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "support",
  "tailnet": { "tags": ["tag:tw-wl-support"], "connects": ["tag:db"] } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01"}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`export default { fetch() { return new Response("ok"); } };`), 0o644)
	caller := `{ "src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"] }`

	// Nothing for the app yet: tagOwners and the tailnet.connects grant are
	// missing; both are in one snippet, with the caller grant.
	e.setPolicy(`{"tagOwners": {"tag:tw": ["autogroup:admin"]}}`)
	out, err := e.call(t, cmdPolicy, d)
	if cliCode(err) != "acl_denied" {
		t.Fatalf("missing policy: %v\n%s", err, out)
	}
	t.Logf("tw policy, nothing in the policy yet:\n%s", out)
	for _, want := range []string{`"tagOwners": {`, `"tag:tw-wl-support"`, `"grants": [`, `"dst": ["tag:db"]`, caller, "Add to your tailnet policy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// --json: the error carries the whole snippet (nothing else is printed).
	if _, err := e.call(t, cmdPolicy, d, "--json"); cliCode(err) != "acl_denied" || !strings.Contains(cliText(err), caller) || !strings.Contains(cliText(err), `"tag:tw-wl-support": [`) {
		t.Errorf("--json, missing policy: %v\n%s", err, cliText(err))
	}
	if ds, _ := e.st.ListDeployments(t.Context(), mgmt.DeploymentFilter{TenantID: e.tenantID}, mgmt.Page{}); len(ds) != 0 {
		t.Fatalf("tw policy deployed: %v", ds)
	}

	// Everything tailworks checks is there: exit 0, and the caller grant is still
	// shown (tailworks can't check it).
	e.setPolicy(`{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:tw-wl-support": ["autogroup:admin", "tag:tw"]},
  "grants": [{"src": ["tag:tw-wl-support"], "dst": ["tag:db"], "ip": ["tcp:80"]}]}`)
	time.Sleep(5 * time.Millisecond) // PolicyMaxAge
	out, err = e.call(t, cmdPolicy, d)
	if err != nil {
		t.Fatalf("complete policy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "The policy has everything tailworks checks") || !strings.Contains(out, caller) || strings.Contains(out, `"tagOwners"`) {
		t.Fatalf("complete policy output:\n%s", out)
	}
	out, err = e.call(t, cmdPolicy, d, "--json")
	var r policyResult
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || !r.OK || !r.Checked || r.CallerGrant != caller || r.Workload != "support" {
		t.Fatalf("--json: %v %+v\n%s", err, r, out)
	}
}
