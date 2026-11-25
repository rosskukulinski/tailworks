package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestOperatorSnippet: the oauth-mode snippet is exactly what README.md
// shows (tag:tw owns tag:tw-mgmt and tag:tw-host; host ⇄ mgmt grants;
// autogroup:admin's admin cap; no nodeAttrs), passes the RT-06 checker with
// the allowance for autogroup:admin, and merges additively into a
// policy that already has rules (existing owners kept, nothing removed).
func TestOperatorSnippet(t *testing.T) {
	t.Parallel()
	s := operatorSnippet()
	wantOwners := map[string][]string{
		"tag:tw":      {"autogroup:admin"},
		"tag:tw-mgmt": {"autogroup:admin", "tag:tw"},
		"tag:tw-host": {"autogroup:admin", "tag:tw"},
	}
	if !reflect.DeepEqual(s.TagOwners, wantOwners) {
		t.Fatalf("tagOwners %v", s.TagOwners)
	}
	gj, _ := json.Marshal(s.Grants)
	for _, want := range []string{
		`{"dst":["tag:tw-mgmt"],"ip":["tcp:443","tcp:80"],"src":["tag:tw-host"]}`,
		`{"dst":["tag:tw-host"],"ip":["tcp:7474"],"src":["tag:tw-mgmt"]}`,
		`{"app":{"` + mgmt.AdminCap + `":[{}]},"dst":["tag:tw-mgmt"],"ip":["tcp:443","tcp:80"],"src":["autogroup:admin"]}`,
	} {
		if !strings.Contains(string(gj), want) {
			t.Fatalf("grants %s lack %s", gj, want)
		}
	}
	if len(s.Grants) != 3 || len(s.NodeAttrs) != 0 {
		t.Fatalf("oauth snippet: %d grants, nodeAttrs %v (no Funnel in oauth mode)", len(s.Grants), s.NodeAttrs)
	}
	if err := policy.Check(policy.Snippet{NodeAttrs: []map[string]any{{"target": []string{mgmt.MgmtTag}, "attr": []string{"funnel"}}}}, adminSrcOptions); err == nil {
		t.Fatal("policy.Check must refuse funnel on " + mgmt.MgmtTag)
	}
	if err := policy.Check(s, adminSrcOptions); err != nil {
		t.Fatalf("RT-06 check: %v", err)
	}
	if err := policy.Check(s, policy.Options{}); err == nil {
		t.Fatal("autogroup:admin as a grant source must need the explicit allowance")
	}
	before := []byte(`{
  // Ross's rules
  "tagOwners": {"tag:tw": ["group:ops"], "tag:tw-mgmt": ["autogroup:admin"], "tag:web": ["autogroup:admin"]},
  "grants": [{"src": ["autogroup:member"], "dst": ["tag:web"], "ip": ["*"]}],
  "ssh": [{"action": "check", "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot"]}],
}`)
	miss, err := policy.Missing(before, s)
	if err != nil {
		t.Fatal(err)
	}
	after, err := policy.MergeWith(before, s, adminSrcOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.VerifyAdditive(before, after, miss); err != nil {
		t.Fatalf("not additive: %v", err)
	}
	if !strings.Contains(string(after), "Ross's rules") || !strings.Contains(string(after), `"group:ops"`) {
		t.Fatalf("merge dropped existing content:\n%s", after)
	}
	if m, _ := policy.Missing(after, tagOwnersNeeded()); !m.Empty() {
		t.Fatalf("after merge, tag:tw still doesn't own the infra tags: %v", m)
	}
	if m, _ := policy.Missing(before, tagOwnersNeeded()); !reflect.DeepEqual(m.TagOwners, map[string][]string{"tag:tw-mgmt": {"tag:tw"}, "tag:tw-host": {"tag:tw"}}) {
		t.Fatalf("missing tag owners: %v", m.TagOwners)
	}
}

// adminSrcOptions lets the operator snippet's autogroup:admin grant source
// through Check (RT-06).
var adminSrcOptions = policy.Options{AllowSrcs: []string{"autogroup:admin"}}

// TestModeChoiceAndTypedErrors: no credential → how to create the OAuth
// client (no removed mode offered); policy_missing_tag_owners carries the
// exact tagOwners snippet.
func TestModeChoiceAndTypedErrors(t *testing.T) {
	t.Parallel()
	e := mintModeRequired()
	text := strings.Join(e.Lines, "\n")
	if e.Code != "mint_mode_required" || !strings.Contains(text, "--oauth-client-file") || !strings.Contains(text, "Auth Keys") || !strings.Contains(text, "tag:tw") ||
		strings.Contains(text, "--wif") || strings.Contains(text, "TS_API_KEY") {
		t.Fatalf("mode choice order/content:\n%s", text)
	}
	pe := policyMissingTagOwners("why", nil, nil)
	fix := strings.Join(pe.Fix, "\n")
	if pe.Code != "policy_missing_tag_owners" || !strings.Contains(fix, `"tag:tw-mgmt": ["autogroup:admin", "tag:tw"]`) || !strings.Contains(fix, `"tag:tw-host": ["autogroup:admin", "tag:tw"]`) {
		t.Fatalf("policy_missing_tag_owners fix:\n%s", fix)
	}
}
