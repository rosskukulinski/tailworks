package mgmt

import (
	"slices"
	"strings"
	"testing"
)

// TestPolicyApplyDefinesConnectsTag: a tailnet.connects target that is
// another tailworks app's tag, not deployed yet (no tagOwners entry), gets
// its tagOwners entry written with the grants to it, instead of leaving the
// grants manual (the control plane rejects a grant to an undefined tag).
func TestPolicyApplyDefinesConnectsTag(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	grantPolicyWrite(e)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/workloads/app/policy"
	body := map[string]any{"bundle": policyApp(t, `["tag:tw-wl-mcp", "tag:db"]`)}

	var plan PolicyApplyResult
	e.c.ok("POST", path+"?dryRun=true", body, &plan)
	if !slices.Equal(plan.Preflight.UndefinedConnects, []string{"tag:tw-wl-mcp"}) || plan.Preflight.OK ||
		!strings.Contains(plan.Preflight.TagOwnersSnippet, `"tag:tw-wl-mcp": ["autogroup:admin", "tag:tw"]`) {
		t.Fatalf("preflight %+v", plan.Preflight)
	}
	if len(plan.Manual) != 0 || !strings.Contains(plan.Added, `"tag:tw-wl-mcp"`) {
		t.Fatalf("plan: added %q, manual %q", plan.Added, plan.Manual)
	}

	var res PolicyApplyResult
	e.c.ok("POST", path, body, &res)
	after, _ := e.policyState()
	if !res.Changed || len(res.Manual) != 0 || !res.Preflight.OK || len(res.Preflight.UndefinedConnects) != 0 {
		t.Fatalf("apply %+v", res)
	}
	for _, want := range []string{`"tag:tw-wl-mcp": ["autogroup:admin", "tag:tw"]`, `"dst": ["tag:tw-wl-mcp"]`, `{"dst": ["tag:db"], "ip": ["tcp:80"]`} {
		if !strings.Contains(after, want) {
			t.Fatalf("policy after apply lacks %s:\n%s", want, after)
		}
	}
}

func TestUndefinedConnectsTags(t *testing.T) {
	t.Parallel()
	pol := &Policy{TagOwners: map[string][]string{"tag:tw-wl-db": {"autogroup:admin"}}}
	src := []string{"tag:tw-wl-app"}
	connects := []string{"tag:db", "tag:tw-wl-mcp", "tag:tw-wl-db", "tag:tw-wl-app", "tag:tw-wl-mcp", "svc:x", "tag:twx"}
	if got := undefinedConnectsTags(Tenant{}, pol, connects, src); !slices.Equal(got, []string{"tag:tw-wl-mcp"}) {
		t.Fatalf("default tenant: %q", got)
	}
	// A team tenant's target may be another team's app: left to the admin.
	if got := undefinedConnectsTags(Tenant{OwnerTag: "tag:tw-t-a"}, pol, connects, src); got != nil {
		t.Fatalf("team tenant: %q", got)
	}
}

func TestMissingGrantsMerge(t *testing.T) {
	t.Parallel()
	pf := PreflightResult{Missing: []PreflightMissing{
		{Src: "tag:a", Dst: "tag:db", Snippet: `{ "src": ["tag:a"], "dst": ["tag:db"], "ip": ["tcp:5432"] }`},
		{Src: "tag:b", Dst: "tag:x", Snippet: `{ "src": ["tag:b"], "dst": ["tag:x"], "ip": ["tcp:80"] }`},
		{Src: "tag:b", Dst: "tag:db", Snippet: `{ "src": ["tag:b"], "dst": ["tag:db"], "ip": ["tcp:5432"] }`},
	}}
	got := pf.MissingGrants()
	if len(got) != 2 || !slices.Equal(got[0].Srcs, []string{"tag:a", "tag:b"}) || got[0].IP != "tcp:5432" || got[1].Dst != "tag:x" {
		t.Fatalf("%+v", got)
	}
	if got[0].Snippet != `{ "src": ["tag:a", "tag:b"], "dst": ["tag:db"], "ip": ["tcp:5432"] }` {
		t.Fatalf("snippet %s", got[0].Snippet)
	}
	if l := grantLabel(map[string]any{"src": []any{"tag:a", "tag:b"}, "dst": []any{"tag:db"}, "ip": []any{"tcp:80"}}); l != "tag:a, tag:b → tag:db tcp:80" {
		t.Fatalf("label %q", l)
	}
}
