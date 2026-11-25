package mgmt

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// TestPreflightSetSnippets (#535): the typed snippet holds every missing
// part, the string fields are its parts' Inline rendering, and the public
// part's tag:tw-public owners win over the tagOwners part's.
func TestPreflightSetSnippets(t *testing.T) {
	t.Parallel()
	pf := PreflightResult{
		MissingTagOwners:  []string{"tag:tw-wl-app", PublicTag},
		UndefinedConnects: []string{"tag:tw-wl-mcp"},
		Missing: []PreflightMissing{missingGrant("tag:tw-wl-app", "tag:db", "tcp:5432"), missingGrant("tag:tw-c-app", "tag:db", "tcp:5432"),
			missingGrant("tag:tw-wl-app", "tag:tw-wl-mcp", "tcp:80")},
		MissingNodeAttrs:  []string{"funnel on " + PublicTag},
		Service:           "svc:app",
		ServiceUnapproved: true,
		workloadTags:      []string{"tag:tw-wl-app"},
	}
	pf.setSnippets(Tenant{OwnerTag: "tag:tw-t-a"})
	s := pf.Snippet
	if s == nil {
		t.Fatal("no typed snippet")
	}
	want := map[string][]string{
		"tag:tw-wl-app": {"autogroup:admin", "tag:tw-t-a"},
		"tag:tw-wl-mcp": {"autogroup:admin", "tag:tw-t-a"},
		PublicTag:       {"autogroup:admin", "tag:tw", "tag:tw-t-a"},
	}
	if !reflect.DeepEqual(s.TagOwners, want) {
		t.Errorf("tagOwners %v, want %v", s.TagOwners, want)
	}
	if len(s.Grants) != 2 || policy.EntryHuJSON(s.Grants[0]) != `{ "src": ["tag:tw-wl-app", "tag:tw-c-app"], "dst": ["tag:db"], "ip": ["tcp:5432"] }` {
		t.Errorf("grants %v", s.Grants)
	}
	if pf.Missing[0].Snippet != `{ "src": ["tag:tw-wl-app"], "dst": ["tag:db"], "ip": ["tcp:5432"] }` || pf.Missing[0].IP != "tcp:5432" {
		t.Errorf("missing[0] %+v", pf.Missing[0])
	}
	if len(s.NodeAttrs) != 1 || s.AutoApprovers.Services["svc:app"][0] != "tag:tw-wl-app" {
		t.Errorf("nodeAttrs %v autoApprovers %v", s.NodeAttrs, s.AutoApprovers)
	}
	if pf.TagOwnersSnippet != `"tagOwners": { "tag:tw-public": ["autogroup:admin", "tag:tw-t-a"], "tag:tw-wl-app": ["autogroup:admin", "tag:tw-t-a"], "tag:tw-wl-mcp": ["autogroup:admin", "tag:tw-t-a"] }` {
		t.Errorf("tagOwnersSnippet %s", pf.TagOwnersSnippet)
	}
	if pf.ServiceSnippet != `"autoApprovers": { "services": { "svc:app": ["tag:tw-wl-app"] } }` {
		t.Errorf("serviceSnippet %s", pf.ServiceSnippet)
	}
	if !strings.Contains(pf.NodeAttrsSnippet, `"nodeAttrs": [ { "target": ["tag:tw-public"], "attr": ["funnel"] } ]`) {
		t.Errorf("nodeAttrsSnippet %s", pf.NodeAttrsSnippet)
	}

	// Nothing missing: no snippet.
	var ok PreflightResult
	ok.setSnippets(Tenant{})
	if ok.Snippet != nil || ok.TagOwnersSnippet != "" {
		t.Errorf("empty preflight: %+v", ok)
	}
}

// TestHostTagOwnersSnippetHeadscale: tw host add's policy_missing_tag_owners
// snippet offers no autogroup:admin on Headscale, which rejects it.
func TestHostTagOwnersSnippetHeadscale(t *testing.T) {
	t.Parallel()
	if s := HostTagOwnersSnippet(Tenant{}); !strings.HasPrefix(s, `"tag:tw-host": ["autogroup:admin", "tag:tw"], // inside the existing "tagOwners" section`) {
		t.Errorf("tailscale: %s", s)
	}
	if s := HostTagOwnersSnippet(Tenant{MintMode: MintHeadscale}); !strings.HasPrefix(s, `"tag:tw-host": ["tag:tw"], //`) {
		t.Errorf("headscale: %s", s)
	}
}
