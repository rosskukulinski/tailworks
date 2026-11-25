package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (e *pubEnv) denied() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writeDenied
}

func (e *pubEnv) denyWrites(on bool) {
	e.mu.Lock()
	e.writeDenied = on
	e.mu.Unlock()
}

// writeSupportApp is an app that needs its tag owned and a grant to tag:db.
func writeSupportApp(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "support",
  "tailnet": { "tags": ["tag:tw-wl-support"], "connects": ["tag:db"] } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01"}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`export default { fetch() { return new Response("ok"); } };`), 0o644)
	return d
}

// TestDeployPolicyWriteDenied (issue #481): acl_denied tells the user tw
// can add the lines; when the OAuth client can't write the policy,
// tw deploy --apply-policy says so in one line and falls back to the
// snippet to paste instead of failing with policy_write_denied.
func TestDeployPolicyWriteDenied(t *testing.T) {
	e := newPubEnv(t)
	app := writeSupportApp(t)
	e.setPolicy(operatorPolicy)

	_, err := e.call(t, cmdDeploy, app, "--no-wait")
	if cliCode(err) != "acl_denied" || !strings.Contains(cliText(err), letTwAddFix) {
		t.Fatalf("plain deploy: %v", err)
	}

	e.denyWrites(true)
	out, err := e.call(t, cmdDeploy, app, "--apply-policy", "--yes", "--no-wait")
	txt := cliText(err)
	if cliCode(err) != "acl_denied" || !strings.Contains(txt, `"tag:tw-wl-support"`) || !strings.Contains(txt, "Policy File › Write") || strings.Contains(txt, letTwAddFix) {
		t.Fatalf("deploy --apply-policy, write denied: %v\n%s\n%s", err, txt, out)
	}
	if !strings.Contains(out, "› policy     not applied: policy_write_denied") {
		t.Fatalf("no one-line reason:\n%s", out)
	}
	if _, n := e.policyNow(); n != 0 {
		t.Fatalf("%d policy writes", n)
	}

	// --allow-missing-grants: deploy without the lines.
	out, err = e.call(t, cmdDeploy, app, "--apply-policy", "--yes", "--allow-missing-grants", "--no-wait")
	if err != nil || !strings.Contains(out, "deploying without the lines") {
		t.Fatalf("deploy --apply-policy --allow-missing-grants, write denied: %v\n%s", err, out)
	}
}
