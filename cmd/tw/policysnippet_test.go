package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestPreflightPolicyLegacy (#535): a preflight from a mgmt without the
// typed snippet prints the same block as one with it, and every printed
// block starts with the merge comment.
func TestPreflightPolicyLegacy(t *testing.T) {
	t.Parallel()
	typed := policy.Snippet{
		TagOwners: map[string][]string{"tag:tw-wl-app": {"autogroup:admin", "tag:tw"}, policy.PublicTag: {"autogroup:admin", "tag:tw"}},
		Grants:    []map[string]any{{"src": []any{"tag:tw-wl-app"}, "dst": []any{"tag:db"}, "ip": []any{"tcp:5432"}}},
		NodeAttrs: []map[string]any{{"target": []any{policy.PublicTag}, "attr": []any{"funnel"}}},
	}
	legacy := mgmt.PreflightResult{
		MissingTagOwners: []string{"tag:tw-wl-app"},
		TagOwnersSnippet: `"tagOwners": { "tag:tw-wl-app": ["autogroup:admin", "tag:tw"] }`,
		Missing:          []mgmt.PreflightMissing{{Src: "tag:tw-wl-app", Dst: "tag:db", Snippet: `{ "src": ["tag:tw-wl-app"], "dst": ["tag:db"], "ip": ["tcp:5432"] }`}},
		MissingNodeAttrs: []string{"funnel on " + policy.PublicTag},
		NodeAttrsSnippet: `"tagOwners": { "tag:tw-public": ["autogroup:admin", "tag:tw"] },
"nodeAttrs": [ { "target": ["tag:tw-public"], "attr": ["funnel"] } ]`,
	}
	withTyped := legacy
	withTyped.Snippet = &typed
	a, b := policySnippet(legacy, nil, nil), policySnippet(withTyped, nil, nil)
	if a != b {
		t.Fatalf("legacy and typed differ:\n%s\n---\n%s", a, b)
	}
	if !strings.HasPrefix(a, policy.MergeComment+"\n") || strings.Count(a, `"tagOwners"`) != 1 {
		t.Fatalf("snippet:\n%s", a)
	}
	fix := strings.Join(preflightFailure(withTyped).Fix, "\n")
	if !strings.Contains(fix, "  "+policy.MergeComment) || strings.Count(fix, `"tagOwners"`) != 1 || strings.Count(fix, `"tag:tw-public"`) != 2 {
		t.Fatalf("fix:\n%s", fix)
	}
	// Over the wire (JSON), the typed snippet still renders the same.
	var wire mgmt.PreflightResult
	b2, _ := json.Marshal(withTyped)
	if err := json.Unmarshal(b2, &wire); err != nil || policySnippet(wire, nil, nil) != a {
		t.Fatalf("decoded: %v\n%s", err, policySnippet(wire, nil, nil))
	}
}
