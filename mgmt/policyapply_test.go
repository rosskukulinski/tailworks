package mgmt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// curPolicyETag is the fake policy's ETag ("p1" until a write).
func (f *fakeTS) curPolicyETag() string {
	if f.policyETag == "" {
		return `"p1"`
	}
	return f.policyETag
}

// policyWrite serves POST /acl (If-Match; Policy File write scope) and
// POST /acl/validate, as the Tailscale API does. Called with f.mu held.
func (f *fakeTS) policyWrite(w http.ResponseWriter, r *http.Request, auth string) {
	if !f.oauthScope(auth, "policy_file") {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"insufficient scope: policy_file"}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(r.URL.Path, "/validate") {
		if f.validateErr != "" {
			json.NewEncoder(w).Encode(map[string]string{"message": f.validateErr})
		}
		return
	}
	f.policyWrites++
	if f.policy == "" {
		f.policy = samplePolicy
	}
	if f.policyConflicts > 0 {
		// Someone saved the policy in the admin console meanwhile.
		f.policyConflicts--
		f.policyETag = fmt.Sprintf(`"c%d"`, f.policyWrites)
		w.WriteHeader(412)
		w.Write([]byte(`{"message":"precondition failed, invalid old hash"}`))
		return
	}
	if r.Header.Get("If-Match") != f.curPolicyETag() {
		w.WriteHeader(412)
		w.Write([]byte(`{"message":"precondition failed, invalid old hash"}`))
		return
	}
	f.policy, f.policyETag = string(body), fmt.Sprintf(`"w%d"`, f.policyWrites)
	w.Header().Set("ETag", f.policyETag)
	w.Write(body)
}

// grantPolicyWrite gives the test env's OAuth client Policy File write.
func grantPolicyWrite(e *testEnv) {
	e.ts.mu.Lock()
	defer e.ts.mu.Unlock()
	c := e.ts.oauthClients[envOAuthClient]
	c.scopes = append(append([]string(nil), c.scopes...), "policy_file")
	e.ts.oauthClients[envOAuthClient] = c
}

func (e *testEnv) policyState() (body string, writes int) {
	e.ts.mu.Lock()
	defer e.ts.mu.Unlock()
	return e.ts.policy, e.ts.policyWrites
}

// operatorOnly is a tailnet with only the operator snippet and someone
// else's rules: the acceptance case of issue #116.
const operatorOnly = `// our tailnet
{
  "tagOwners": {
    "tag:tw": ["autogroup:admin"],
    "tag:tw-mgmt": ["autogroup:admin", "tag:tw"],
    "tag:tw-host": ["autogroup:admin", "tag:tw"],
    "tag:db": ["autogroup:admin"], // the database
  },
  "grants": [
    {"src": ["group:eng"], "dst": ["tag:db"], "ip": ["tcp:5432"]}, // keep me
  ],
}`

func policyApp(t *testing.T, connects string) []byte {
	return makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"name": "app", "tailnet": {"tags": ["tag:tw-wl-app"], "connects": ` + connects + `}}`,
		"dist/index.js":   "export default {}",
	})
}

// TestPolicyApply: plan, refuse without the write scope, write additively
// with If-Match, audit, then nothing more to do.
func TestPolicyApply(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/workloads/app/policy"
	body := map[string]any{"bundle": policyApp(t, `["tag:db"]`)}

	var plan PolicyApplyResult
	e.c.ok("POST", path+"?dryRun=true", body, &plan)
	if !plan.DryRun || plan.Changed || plan.Preflight.OK || !strings.Contains(plan.Added, `"tag:tw-wl-app"`) || !strings.Contains(plan.Added, `"dst": ["tag:db"]`) {
		t.Fatalf("plan %+v", plan)
	}
	if _, n := e.policyState(); n != 0 {
		t.Fatalf("dryRun wrote the policy (%d writes)", n)
	}

	// The client lacks Policy File write: refused, audited, nothing written.
	e.c.fail("POST", path, body, 403, "policy_write_denied")
	if p, _ := e.policyState(); p != operatorOnly {
		t.Fatalf("a refused write changed the policy:\n%s", p)
	}

	grantPolicyWrite(e)
	var res PolicyApplyResult
	e.c.ok("POST", path, body, &res)
	if !res.Changed || res.OldETag != `"p1"` || res.NewETag == "" || !res.Preflight.OK {
		t.Fatalf("apply %+v", res)
	}
	after, writes := e.policyState()
	for _, want := range []string{"// our tailnet", "// the database", "// keep me", `"group:eng"`, `"tag:tw-wl-app"`} {
		if !strings.Contains(after, want) {
			t.Fatalf("after apply the policy lacks %q:\n%s", want, after)
		}
	}
	added := policy.Snippet{TagOwners: map[string][]string{"tag:tw-wl-app": {"autogroup:admin", "tag:tw"}},
		Grants: []map[string]any{{"src": []string{"tag:tw-wl-app"}, "dst": []string{"tag:db"}, "ip": []string{"tcp:80"}},
			{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-app"}, "ip": []string{"tcp:80"}}}} // callergrant.go
	if err := policy.VerifyAdditive([]byte(operatorOnly), []byte(after), added); err != nil {
		t.Fatalf("not purely additive: %v", err)
	}

	// Idempotent: the second apply writes nothing.
	var again PolicyApplyResult
	e.c.ok("POST", path, body, &again)
	if again.Changed || again.Added != "" || !again.Preflight.OK {
		t.Fatalf("second apply %+v", again)
	}
	if _, n := e.policyState(); n != writes {
		t.Fatalf("second apply wrote again (%d → %d)", writes, n)
	}

	recs := auditKinds(t, e, tn, "policy_apply")
	if len(recs) != 2 || recs[0].Code != "policy_write_denied" || recs[1].Code != "" || recs[1].ActingFor == "" ||
		!strings.Contains(string(recs[1].Detail), "tag:tw-wl-app") || !strings.Contains(string(recs[1].Detail), `"oldETag"`) {
		t.Fatalf("policy_apply audit records: %+v", recs)
	}
}

// TestPolicyApplyConflict: a 412 is retried once from a fresh read; a
// second one is 409 policy_conflict with nothing written.
func TestPolicyApplyConflict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		conflicts, writes int
		code              string
	}{{1, 2, ""}, {2, 2, "policy_conflict"}} {
		t.Run(fmt.Sprint(tc.conflicts), func(t *testing.T) {
			e := newTestEnv(t)
			e.ts.policy = operatorOnly
			grantPolicyWrite(e)
			tn := v1Tenant(t, e, "t.ts.net", nil)
			e.ts.mu.Lock()
			e.ts.policyConflicts = tc.conflicts
			e.ts.mu.Unlock()
			path := "/v1/tenants/" + tn + "/workloads/app/policy"
			body := map[string]any{"bundle": policyApp(t, `["tag:db"]`)}
			if tc.code != "" {
				e.c.fail("POST", path, body, 409, tc.code)
				if p, _ := e.policyState(); p != operatorOnly {
					t.Fatalf("policy changed after a conflict:\n%s", p)
				}
			} else {
				var res PolicyApplyResult
				e.c.ok("POST", path, body, &res)
				if !res.Changed || res.OldETag != `"c1"` {
					t.Fatalf("retry %+v (want the re-read ETag)", res)
				}
			}
			if _, n := e.policyState(); n != tc.writes {
				t.Fatalf("%d writes, want %d (one retry, never more)", n, tc.writes)
			}
		})
	}
}

// TestPolicyApplyHostile (RT-06): a bundle whose connects would need a
// grant to "*" or the internet gets the tw-owned part written and the rest
// reported as manual; validation failures write nothing.
func TestPolicyApplyHostile(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	grantPolicyWrite(e)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/workloads/app/policy"

	e.ts.mu.Lock()
	e.ts.validateErr = "tag:nope not defined"
	e.ts.mu.Unlock()
	e.c.fail("POST", path, map[string]any{"bundle": policyApp(t, `["tag:db"]`)}, 500, "policy_apply_failed")
	if p, n := e.policyState(); p != operatorOnly || n != 0 {
		t.Fatalf("an invalid policy was written (%d writes):\n%s", n, p)
	}
	e.ts.mu.Lock()
	e.ts.validateErr = ""
	e.ts.mu.Unlock()

	var res PolicyApplyResult
	e.c.ok("POST", path, map[string]any{"bundle": policyApp(t, `["*", "autogroup:internet", "tag:db"]`)}, &res)
	after, _ := e.policyState()
	if !res.Changed || res.Preflight.OK || len(res.Manual) != 2 {
		t.Fatalf("apply %+v", res)
	}
	for _, m := range res.Manual {
		if !strings.Contains(m, "not written") {
			t.Fatalf("manual %q", m)
		}
	}
	for _, bad := range []string{`"dst":["*"]`, `"dst": ["*"]`, `autogroup:internet`} {
		if strings.Contains(after, bad) {
			t.Fatalf("tailworks wrote %s:\n%s", bad, after)
		}
	}
	if !strings.Contains(after, `"tag:tw-wl-app"`) || !strings.Contains(after, "// keep me") {
		t.Fatalf("policy after apply:\n%s", after)
	}

	// A connects tag the policy doesn't define (the control plane would
	// reject the whole policy): its grant is manual, the rest is written.
	var und PolicyApplyResult
	e.c.ok("POST", "/v1/tenants/"+tn+"/workloads/other/policy", map[string]any{"bundle": makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"name": "other", "tailnet": {"tags": ["tag:tw-wl-other"], "connects": ["tag:nowhere"]}}`,
		"dist/index.js":   "export default {}",
	})}, &und)
	after, _ = e.policyState()
	if !und.Changed || len(und.Manual) != 1 || !strings.Contains(und.Manual[0], "tag:nowhere has no tagOwners entry") ||
		!strings.Contains(after, `"tag:tw-wl-other"`) || strings.Contains(after, "tag:nowhere") {
		t.Fatalf("undefined dst: %+v\n%s", und, after)
	}
}

// TestPolicyApplyAdminOnly: tenant admins may deploy, but the tailnet
// policy is the tailnet admins'.
func TestPolicyApplyAdminOnly(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	body := map[string]any{"bundle": policyApp(t, `["tag:db"]`)}
	for _, q := range []string{"", "?dryRun=true"} {
		if st, eb := e.call(nodeTeamA, "POST", "/v1/tenants/"+e.tn+"/workloads/app/policy"+q, body, nil); st != 403 {
			t.Fatalf("tenant admin %s: %d %v", q, st, eb)
		}
	}
	if _, n := e.policyState(); n != 0 {
		t.Fatalf("a tenant admin wrote the policy")
	}
}
