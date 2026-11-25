package mgmt

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// TestPolicyApplyService (issue #295): tw policy --apply writes the
// workload's autoApprovers.services entry, for its own svc: name and its
// own tags only, next to the tagOwners it already writes; the preflight
// then passes and a second apply writes nothing.
func TestPolicyApplyService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = operatorOnly
	grantPolicyWrite(e)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/workloads/support/policy"
	body := map[string]any{"bundle": makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"name": "support", "tailnet": {"tags": ["tag:tw-wl-support"], "service": true}}`,
		"dist/index.js":   "export default {}",
	})}

	var plan PolicyApplyResult
	e.c.ok("POST", path+"?dryRun=true", body, &plan)
	if !plan.Preflight.ServiceUnapproved || len(plan.Manual) != 0 ||
		!strings.Contains(plan.Added, `"autoApprovers"`) || !strings.Contains(plan.Added, `"svc:support": ["tag:tw-wl-support"]`) {
		t.Fatalf("plan %+v", plan)
	}

	var res PolicyApplyResult
	e.c.ok("POST", path, body, &res)
	if !res.Changed || !res.Preflight.OK || res.Preflight.ServiceUnapproved || len(res.Manual) != 0 {
		t.Fatalf("apply %+v", res)
	}
	after, writes := e.policyState()
	added := policy.Snippet{
		TagOwners:     map[string][]string{"tag:tw-wl-support": {"autogroup:admin", "tag:tw"}},
		AutoApprovers: policy.AutoApprovers{Services: map[string][]string{"svc:support": {"tag:tw-wl-support"}}},
		// The caller grant to the node's own tag (callergrant.go); the one to svc:support stays manual.
		Grants: []map[string]any{{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-support"}, "ip": []string{"tcp:80"}}},
	}
	if err := policy.VerifyAdditive([]byte(operatorOnly), []byte(after), added); err != nil {
		t.Fatalf("not purely additive: %v\n%s", err, after)
	}

	var again PolicyApplyResult
	e.c.ok("POST", path, body, &again)
	if again.Changed || again.Added != "" || !again.Preflight.OK {
		t.Fatalf("second apply %+v", again)
	}
	if _, n := e.policyState(); n != writes {
		t.Fatalf("second apply wrote again (%d → %d)", writes, n)
	}
}

// serviceApprover writes nothing wider than the workload's own Service and
// tags: anything else in the preflight's snippet stays manual.
func TestServiceApproverNarrow(t *testing.T) {
	t.Parallel()
	defined := func(string) bool { return true }
	approvers := func(svc map[string][]string) *policy.Snippet {
		return &policy.Snippet{AutoApprovers: policy.AutoApprovers{Services: svc}}
	}
	pf := func(snip *policy.Snippet) PreflightResult {
		return PreflightResult{Service: "svc:support", ServiceUnapproved: true, ServiceSnippet: snip.Inline(), Snippet: snip, workloadTags: []string{"tag:tw-wl-support"}}
	}
	ok := approvers(map[string][]string{"svc:support": {"tag:tw-wl-support"}})
	cases := map[string]struct {
		pf   PreflightResult
		o    policy.Options
		def  func(string) bool
		want string // "" = written
	}{
		"own":           {pf(ok), policy.Options{}, defined, ""},
		"tag:tw":        {pf(approvers(map[string][]string{"svc:support": {"tag:tw"}})), policy.Options{}, defined, "isn't one of the workload's tags"},
		"other service": {pf(approvers(map[string][]string{"svc:other": {"tag:tw-wl-support"}})), policy.Options{}, defined, "isn't for svc:support alone"},
		"two services":  {pf(approvers(map[string][]string{"svc:support": {"tag:tw-wl-support"}, "svc:x": {"tag:tw-wl-support"}})), policy.Options{}, defined, "isn't for svc:support alone"},
		"undefined tag": {pf(ok), policy.Options{}, func(string) bool { return false }, "has no tagOwners entry"},
		"headscale":     {pf(ok), policy.Options{Headscale: true}, defined, "Headscale"},
		"no tags":       {PreflightResult{Service: "svc:support", ServiceUnapproved: true, ServiceSnippet: ok.Inline(), Snippet: ok}, policy.Options{}, defined, "isn't one of the workload's tags"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snip, manual := preflightSnippet(tc.pf, tc.o, tc.def)
			if tc.want == "" {
				if len(manual) != 0 || snip.AutoApprovers.Services["svc:support"] == nil {
					t.Fatalf("not written: %+v %q", snip, manual)
				}
				return
			}
			if len(snip.AutoApprovers.Services) != 0 || len(manual) != 1 || !strings.Contains(manual[0], tc.want) || !strings.Contains(manual[0], tc.pf.ServiceSnippet) {
				t.Fatalf("snippet %+v, manual %q (want %q)", snip, manual, tc.want)
			}
		})
	}
}
