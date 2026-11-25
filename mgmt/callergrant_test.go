package mgmt

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

const appTag = "tag:tw-wl-app"

// newAppSnippet is what a first tw deploy --apply-policy adds for the app.
func newAppSnippet(tags ...string) policy.Snippet {
	s := policy.Snippet{TagOwners: map[string][]string{}}
	for _, t := range tags {
		s.TagOwners[t] = []string{"autogroup:admin", "tag:tw"}
	}
	return s
}

func mustPolicy(t *testing.T, s string) *Policy {
	t.Helper()
	p, err := ParsePolicy([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCallerGrantRule: the caller grant is written only with a new app's
// own tailworks tag, and never when people already reach the app.
func TestCallerGrantRule(t *testing.T) {
	const base = `{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:db": ["autogroup:admin"]},
		"grants": [{"src": ["group:eng"], "dst": ["tag:db"], "ip": ["tcp:5432"]}]}`
	for _, tc := range []struct {
		name   string
		pol    string
		tags   []string
		keep   policy.Snippet
		opts   policy.Options
		want   bool   // the grant is written
		manual string // "" = nothing said
	}{
		{name: "a new app", pol: base, tags: []string{appTag}, keep: newAppSnippet(appTag), want: true},
		{name: "two new tags", pol: base, tags: []string{appTag, "tag:tw-wl-app2"}, keep: newAppSnippet(appTag, "tag:tw-wl-app2"), want: true},
		{name: "the policy can't be read", pol: "", tags: []string{appTag}, keep: newAppSnippet(appTag)},
		{name: "no tags", pol: base, keep: newAppSnippet(appTag)},
		{name: "the tag is already defined (another app's, or the admin removed the grant)",
			pol:  `{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:tw-wl-app": ["tag:tw"]}}`,
			tags: []string{appTag}, keep: policy.Snippet{}, manual: "already in tagOwners"},
		{name: "one of two tags is already defined",
			pol:  `{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:tw-wl-app": ["tag:tw"]}}`,
			tags: []string{"tag:tw-wl-new", appTag}, keep: newAppSnippet("tag:tw-wl-new"), manual: "already in tagOwners"},
		{name: "the admin already lets a group call it",
			pol: `{"tagOwners": {"tag:tw-wl-app": ["tag:tw"]},
				"grants": [{"src": ["group:eng"], "dst": ["tag:tw-wl-app"], "ip": ["tcp:80"]}]}`,
			tags: []string{appTag}, keep: policy.Snippet{}},
		{name: "the same grant is there",
			pol: `{"tagOwners": {"tag:tw-wl-app": ["tag:tw"]},
				"grants": [{"src": ["autogroup:member"], "dst": ["tag:tw-wl-app"], "ip": ["tcp:80"]}]}`,
			tags: []string{appTag}, keep: policy.Snippet{}},
		{name: "another app's grant to it is not the admin's choice of callers",
			pol: `{"tagOwners": {"tag:tw-wl-app": ["tag:tw"]},
				"grants": [{"src": ["tag:tw-wl-other"], "dst": ["tag:tw-wl-app"], "ip": ["tcp:80"]}]}`,
			tags: []string{appTag}, keep: policy.Snippet{}, manual: "already in tagOwners"},
		{name: "the default allow-all grant", pol: `{"grants": [{"src": ["*"], "dst": ["*"], "ip": ["*"]}]}`,
			tags: []string{appTag}, keep: newAppSnippet(appTag)},
		{name: "a group reaches every node", pol: `{"grants": [{"src": ["group:eng"], "dst": ["*"], "ip": ["tcp:80"]}]}`,
			tags: []string{appTag}, keep: newAppSnippet(appTag)},
		{name: "the default allow-all ACL", pol: `{"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]}`,
			tags: []string{appTag}, keep: newAppSnippet(appTag)},
		{name: "an ACL to the tag", pol: `{"acls": [{"action": "accept", "src": ["group:eng"], "dst": ["tag:tw-wl-app:80"]}]}`,
			tags: []string{appTag}, keep: newAppSnippet(appTag)},
		{name: "an app-capability grant to * opens no port", pol: `{"grants": [{"src": ["*"], "dst": ["*"], "app": {"example.com/cap/x": [{}]}}]}`,
			tags: []string{appTag}, keep: newAppSnippet(appTag), want: true},
		{name: "not a tailworks tag", pol: base, tags: []string{"tag:web"}, keep: newAppSnippet("tag:web"), manual: "isn't an app tag"},
		{name: "a tag that isn't tailworks' but looks close", pol: base, tags: []string{"tag:twx"}, keep: newAppSnippet("tag:twx"), manual: "isn't an app tag"},
		{name: "mgmt's tag", pol: base, tags: []string{MgmtTag}, keep: newAppSnippet(MgmtTag), manual: "isn't an app tag"},
		{name: "the host tag", pol: base, tags: []string{HostTag}, keep: newAppSnippet(HostTag), manual: "isn't an app tag"},
		{name: "tag:tw", pol: `{}`, tags: []string{"tag:tw"}, keep: newAppSnippet("tag:tw"), manual: "isn't an app tag"},
		{name: "the public tag", pol: base, tags: []string{PublicTag}, keep: newAppSnippet(PublicTag), manual: "isn't an app tag"},
		{name: "the tag isn't being added", pol: base, tags: []string{appTag}, keep: policy.Snippet{}, manual: "isn't being added"},
		{name: "Headscale accepts autogroup:member", pol: base, tags: []string{appTag}, keep: newAppSnippet(appTag),
			opts: policy.Options{Headscale: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pol *Policy
			if tc.pol != "" {
				pol = mustPolicy(t, tc.pol)
			}
			g, why := callerGrant(tc.tags, tc.keep, pol, tc.opts)
			if (g != nil) != tc.want {
				t.Fatalf("grant %v, want written=%v (%s)", g, tc.want, why)
			}
			if (why == "") != (tc.manual == "") || !strings.Contains(why, tc.manual) {
				t.Fatalf("manual %q, want it to contain %q", why, tc.manual)
			}
			if g == nil {
				return
			}
			if why != "" {
				t.Fatalf("a written grant has a manual line: %s", why)
			}
			want := map[string]any{"src": []string{"autogroup:member"}, "dst": tc.tags, "ip": []string{"tcp:80"}}
			if a, b := string(mustJSON(g)), string(mustJSON(want)); a != b {
				t.Fatalf("grant %s, want %s", a, b)
			}
			// It passes Check only with the relaxation.
			one := policy.Snippet{Grants: []map[string]any{g}}
			if policy.Check(one, tc.opts) == nil {
				t.Fatal("the caller grant passes policy.Check without callerGrantOptions")
			}
			if err := policy.Check(one, callerGrantOptions(tc.opts)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCallerGrantBundleSourcesStillRefused (RT-06): the relaxation is for
// mgmt's own grant. A grant that comes out of a bundle's preflight with a
// source outside tag:tw* is never kept, whatever it targets.
func TestCallerGrantBundleSourcesStillRefused(t *testing.T) {
	for _, src := range []string{"autogroup:member", "*", "autogroup:tagged", "group:eng", "alice@example.com", "tag:db"} {
		pf := PreflightResult{MissingTagOwners: []string{appTag}, Missing: []PreflightMissing{missingGrant(src, appTag, "tcp:80")}}
		pf.setSnippets(Tenant{})
		keep, manual := preflightSnippet(pf, policy.Options{}, func(string) bool { return false })
		if len(keep.Grants) != 0 || len(manual) != 1 || !strings.Contains(manual[0], "not written") {
			t.Fatalf("a bundle's grant from %s was kept: %v (manual %v)", src, keep.Grants, manual)
		}
		// With the caller grant next to it, the snippet still has only
		// mgmt's own grant from outside tag:tw*.
		cg, _ := callerGrant([]string{appTag}, keep, mustPolicy(t, `{}`), policy.Options{})
		if cg == nil {
			t.Fatal("no caller grant for a new app")
		}
		keep.Grants = append(keep.Grants, cg)
		if !onlyCallerGrant(keep, cg) {
			t.Fatal("onlyCallerGrant refuses the caller grant alone")
		}
		hostile := map[string]any{"src": []string{src}, "dst": []string{appTag}, "ip": []string{"tcp:80"}}
		if string(mustJSON(hostile)) == string(mustJSON(cg)) {
			hostile["ip"] = []string{"*"} // the caller grant's source, another port
		}
		keep.Grants = append(keep.Grants, hostile)
		if onlyCallerGrant(keep, cg) {
			t.Fatalf("onlyCallerGrant lets a second grant from %s through", src)
		}
	}
}

// TestPolicyApplyCallerGrant: the endpoint plans and writes the caller
// grant with a new app's tag, names it in the result, and never for a tag
// the policy already defines.
func TestPolicyApplyCallerGrant(t *testing.T) {
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	grantPolicyWrite(e)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/workloads/app/policy"
	body := map[string]any{"bundle": policyApp(t, `[]`)}
	const grant = `{ "src": ["autogroup:member"], "dst": ["tag:tw-wl-app"], "ip": ["tcp:80"] }`

	var plan PolicyApplyResult
	e.c.ok("POST", path+"?dryRun=true", body, &plan)
	if plan.CallerGrant != grant || !strings.Contains(plan.Added, grant) || len(plan.Manual) != 0 {
		t.Fatalf("plan %+v", plan)
	}
	var res PolicyApplyResult
	e.c.ok("POST", path, body, &res)
	after, _ := e.policyState()
	if !res.Changed || res.CallerGrant != grant {
		t.Fatalf("apply %+v", res)
	}
	added := newAppSnippet(appTag)
	added.Grants = []map[string]any{{"src": []string{"autogroup:member"}, "dst": []string{appTag}, "ip": []string{"tcp:80"}}}
	if err := policy.VerifyAdditive([]byte(operatorOnly), []byte(after), added); err != nil {
		t.Fatalf("not exactly the tag and the caller grant: %v\n%s", err, after)
	}
	recs := auditKinds(t, e, tn, "policy_apply")
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), "autogroup:member") {
		t.Fatalf("the audit record doesn't carry the caller grant: %+v", recs)
	}

	// A second app whose bundle claims the first app's tag as its own, and
	// needs something else written: the tag is defined, so no caller
	// grant... and here people already reach it, so nothing is said.
	other := map[string]any{"bundle": makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"name": "other", "tailnet": {"tags": ["tag:tw-wl-app"], "connects": ["tag:db"]}}`,
		"dist/index.js":   "export default {}",
	})}
	var o PolicyApplyResult
	e.c.ok("POST", "/v1/tenants/"+tn+"/workloads/other/policy", other, &o)
	if !o.Changed || o.CallerGrant != "" || len(o.Manual) != 0 || strings.Contains(o.Added, "autogroup:member") {
		t.Fatalf("second app on the same tag: %+v", o)
	}

	// The admin removes the caller grant; an apply that has something else
	// to write doesn't put it back, and says so.
	e.ts.mu.Lock()
	e.ts.policy = strings.Replace(operatorOnly, `"tag:db": ["autogroup:admin"], // the database`,
		`"tag:db": ["autogroup:admin"], "tag:tw-wl-app": ["autogroup:admin", "tag:tw"],`, 1)
	e.ts.mu.Unlock()
	var back PolicyApplyResult
	e.c.ok("POST", "/v1/tenants/"+tn+"/workloads/app/policy", map[string]any{"bundle": policyApp(t, `["tag:db"]`)}, &back)
	after, _ = e.policyState()
	if !back.Changed || back.CallerGrant != "" || strings.Contains(after, "autogroup:member") ||
		len(back.Manual) != 1 || !strings.Contains(back.Manual[0], "callers:") || !strings.Contains(back.Manual[0], grant) {
		t.Fatalf("after the admin removed the caller grant: %+v\n%s", back, after)
	}
}
