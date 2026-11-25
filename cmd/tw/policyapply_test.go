package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// writePolicy is the fake API's POST /acl: If-Match against the ETag GET
// serves (the policy's length), as Tailscale does with its hash.
func (e *pubEnv) writePolicy(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.Header.Get("If-Match") != `"`+strconv.Itoa(len(e.policy))+`"` {
		w.WriteHeader(412)
		w.Write([]byte(`{"message":"precondition failed, invalid old hash"}`))
		return
	}
	e.policy = string(body)
	e.policyWrites++
	w.Header().Set("ETag", `"`+strconv.Itoa(len(e.policy))+`"`)
	w.Write(body)
}

func (e *pubEnv) policyNow() (string, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.policy, e.policyWrites
}

const operatorPolicy = `// ours
{
  "tagOwners": {
    "tag:tw": ["autogroup:admin"],
    "tag:db": ["autogroup:admin"],
  },
  "grants": [
    {"src": ["group:eng"], "dst": ["tag:db"], "ip": ["tcp:5432"]}, // keep me
  ],
}`

// TestCLIPolicyApply (issue #116): tw policy --apply prints the lines it
// will add, refuses without --yes when there is no terminal, then writes
// only those lines (If-Match) and passes; tw deploy --apply-policy does the
// same before deploying.
func TestCLIPolicyApply(t *testing.T) {
	e := newPubEnv(t)
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "support",
  "tailnet": { "tags": ["tag:tw-wl-support"], "connects": ["tag:db"] } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01"}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`export default { fetch() { return new Response("ok"); } };`), 0o644)
	e.setPolicy(operatorPolicy)

	// No terminal, no --yes: the diff is printed, nothing is written.
	out, err := e.call(t, cmdPolicy, d, "--apply")
	if cliCode(err) != "confirmation_required" || !strings.Contains(cliText(err), "tw policy --apply --yes") {
		t.Fatalf("--apply without --yes: %v\n%s", err, out)
	}
	for _, want := range []string{`+ "tagOwners": {`, `+   "tag:tw-wl-support": ["autogroup:admin", "tag:tw"],`, `"dst": ["tag:db"]`,
		`"src": ["autogroup:member"]`, "lets every user in your tailnet call support"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff lacks %q:\n%s", want, out)
		}
	}
	if p, n := e.policyNow(); n != 0 || p != operatorPolicy {
		t.Fatalf("refused --apply wrote the policy (%d writes)", n)
	}

	out, err = e.call(t, cmdPolicy, d, "--apply", "--yes")
	if err != nil || !strings.Contains(out, "✓ policy     applied") || !strings.Contains(out, "The policy now has everything tailworks checks") {
		t.Fatalf("--apply --yes: %v\n%s", err, out)
	}
	// One report: the header and checks once, no ETags.
	if strings.Count(out, "tw policy: what support needs") != 1 || strings.Count(out, "? callers") != 1 || strings.Contains(out, "If-Match") {
		t.Fatalf("--apply --yes repeats itself:\n%s", out)
	}
	t.Logf("tw policy --apply --yes:\n%s", out)
	after, n := e.policyNow()
	if n != 1 || !strings.Contains(after, "// keep me") || !strings.Contains(after, "// ours") {
		t.Fatalf("policy after apply (%d writes):\n%s", n, after)
	}
	want := policy.Snippet{TagOwners: map[string][]string{"tag:tw-wl-support": {"autogroup:admin", "tag:tw"}},
		Grants: []map[string]any{{"src": []string{"tag:tw-wl-support"}, "dst": []string{"tag:db"}, "ip": []string{"tcp:80"}},
			{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-support"}, "ip": []string{"tcp:80"}}}} // the caller grant (#320)
	if err := policy.VerifyAdditive([]byte(operatorPolicy), []byte(after), want); err != nil {
		t.Fatal(err)
	}
	// The caller grant is in the diff, said in plain words, and not left
	// in a snippet to paste.
	for _, w := range []string{`+   { "src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"] },`,
		"lets every user in your tailnet call support", "every user in your tailnet may call support; run tw deploy"} {
		if !strings.Contains(out, w) {
			t.Fatalf("--apply --yes output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "who may call it") || strings.Contains(out, "make sure a grant like this exists") {
		t.Fatalf("--apply --yes still asks for the caller grant to be pasted:\n%s", out)
	}
	// Again: nothing missing, nothing written.
	time.Sleep(5 * time.Millisecond) // PolicyMaxAge
	if out, err := e.call(t, cmdPolicy, d, "--apply", "--json"); err != nil {
		t.Fatalf("second --apply: %v\n%s", err, out)
	} else {
		var r policyResult
		if json.Unmarshal([]byte(out), &r) != nil || !r.OK || r.Applied != nil {
			t.Fatalf("second --apply --json: %+v\n%s", r, out)
		}
	}
	if _, n := e.policyNow(); n != 1 {
		t.Fatalf("second --apply wrote (%d writes)", n)
	}

	// tw deploy --apply-policy: a public app needs tag:tw-public and funnel.
	if _, err := e.call(t, cmdTenantSet, e.tenantID, "--public-ingress", "allow"); err != nil {
		t.Fatal(err)
	}
	app := writePubApp(t)
	if _, err := e.call(t, cmdDeploy, app, "--apply-policy", "--no-wait"); cliCode(err) != "confirmation_required" {
		t.Fatalf("deploy --apply-policy without --yes: %v", err)
	}
	out, err = e.call(t, cmdDeploy, app, "--apply-policy", "--yes", "--no-wait")
	if err != nil || !strings.Contains(out, "✓ policy     applied") || !strings.Contains(out, "✓ deploying  support@h1") {
		t.Fatalf("deploy --apply-policy --yes: %v\n%s", err, out)
	}
	after, _ = e.policyNow()
	if !strings.Contains(after, `"tag:tw-public"`) || !strings.Contains(after, `"funnel"`) || !strings.Contains(after, "// keep me") {
		t.Fatalf("policy after deploy --apply-policy:\n%s", after)
	}
}
