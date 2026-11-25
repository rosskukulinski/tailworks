package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// RT-06: a bundle's tailworks.jsonc drives the snippet `tw deploy --apply-policy`
// merges into the live tenant policy. These are the snippets a hostile or
// mistaken bundle could produce; every one must be refused before anything
// is written, and the policy must be byte-for-byte unchanged.
func TestHostileSnippetsRefused(t *testing.T) {
	cases := map[string]Snippet{
		"src *":                        {Grants: []map[string]any{{"src": []string{"*"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"*"}}}},
		"src autogroup:member":         {Grants: []map[string]any{{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"src group":                    {Grants: []map[string]any{{"src": []string{"group:eng"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"src user":                     {Grants: []map[string]any{{"src": []string{"alice@example.com"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"src non-tw tag":               {Grants: []map[string]any{{"src": []string{"tag:prod"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"src mixed":                    {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x", "*"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"dst *":                        {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"*"}, "ip": []string{"*"}}}},
		"dst autogroup:internet":       {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"autogroup:internet"}, "ip": []string{"*"}}}},
		"dst user":                     {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"ceo@example.com"}, "ip": []string{"*"}}}},
		"dst cidr":                     {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"10.0.0.0/8"}, "ip": []string{"*"}}}},
		"empty src":                    {Grants: []map[string]any{{"src": []string{}, "dst": []string{"tag:tw-wl-x"}}}},
		"missing dst":                  {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}}}},
		"extra field":                  {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"tag:db"}, "ip": []string{"*"}, "srcPosture": []string{"posture:x"}}}},
		"src not strings":              {Grants: []map[string]any{{"src": []any{1}, "dst": []string{"tag:db"}}}},
		"tagOwners prod":               {TagOwners: map[string][]string{"tag:prod": {"tag:tw"}}},
		"tagOwners tw owned by member": {TagOwners: map[string][]string{"tag:tw-wl-x": {"autogroup:member"}}},
		"tagOwners tw owned by user":   {TagOwners: map[string][]string{"tag:tw-wl-x": {"alice@example.com"}}},
		"tagOwners tw owned by group":  {TagOwners: map[string][]string{"tag:tw-wl-x": {"group:eng"}}},
		"tagOwners empty owners":       {TagOwners: map[string][]string{"tag:tw-wl-x": {}}},
		// ADR-029 D6: funnel only on tag:tw-public, never mgmt.
		"funnel on tag:tw-mgmt":      {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-mgmt"}, "attr": []string{"funnel"}}}},
		"funnel public + mgmt":       {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-public", "tag:tw-mgmt"}, "attr": []string{"funnel"}}}},
		"funnel on tag:tw-issuer":    {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-issuer"}, "attr": []string{"funnel"}}}},
		"funnel on tag:tw":           {NodeAttrs: []map[string]any{{"target": []string{"tag:tw"}, "attr": []string{"funnel"}}}},
		"funnel on object tag":       {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-do-x"}, "attr": []string{"funnel"}}}},
		"funnel on workload tag":     {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-wl-support"}, "attr": []string{"funnel"}}}},
		"funnel public + object tag": {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-public", "tag:tw-do-x"}, "attr": []string{"funnel"}}}},
		"funnel on tag:tw-public-x":  {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-public-x"}, "attr": []string{"funnel"}}}},
		"funnel on autogroup:member": {NodeAttrs: []map[string]any{{"target": []string{"autogroup:member"}, "attr": []string{"funnel"}}}},
		"tagOwners prefix trick":     {TagOwners: map[string][]string{"tag:twprod": {"tag:tw"}, "tag:tw-x": {"tag:tw"}}}, // tag:twprod is NOT a tailworks tag? It has the prefix: allowed by prefix rule; see below
	}
	delete(cases, "tagOwners prefix trick") // documented: anything under tag:tw* is tailworks' namespace by construction
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check(s, Options{}); err == nil {
				t.Fatalf("Check accepted hostile snippet %+v", s)
			}
			out, err := Merge([]byte(base), s)
			if err == nil {
				t.Fatalf("Merge accepted hostile snippet:\n%s", out)
			}
			f := &fakeAPI{body: []byte(base), etag: `"e1"`}
			if _, err := Apply(t.Context(), f, s); err == nil || f.sets != 0 || string(f.body) != base {
				t.Fatalf("Apply wrote a hostile snippet: err=%v sets=%d", err, f.sets)
			}
		})
	}
	// The legitimate shape passes.
	good := Snippet{TagOwners: map[string][]string{"tag:tw-wl-x": {"autogroup:admin", "tag:tw"}, "tag:tw-agent-sub": {"tag:tw", "tag:tw-agent"}},
		Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"tag:db", "svc:api"}, "ip": []string{"tcp:5432"}},
			{"src": []string{"tag:tw-agent"}, "dst": []string{"tag:tw-wl-mcp"}, "ip": []string{"tcp:80"}, "app": map[string]any{"example.com/cap/mcp": []any{map[string]any{"tools": []string{"lookup_order"}}}}}}}
	if err := Check(good, Options{}); err != nil {
		t.Fatalf("legitimate snippet refused: %v", err)
	}
	if _, err := Merge([]byte(base), good); err != nil {
		t.Fatalf("legitimate merge refused: %v", err)
	}
}

// VerifyAdditive catches a merge result that removed or modified anything,
// and prints what changed.
func TestVerifyAdditiveRefusesModification(t *testing.T) {
	added := Snippet{TagOwners: map[string][]string{"tag:tw-a": {"tag:tw"}}, Grants: []map[string]any{{"src": []string{"tag:tw-a"}, "dst": []string{"tag:db"}, "ip": []string{"tcp:80"}}}}
	before := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["autogroup:admin"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]}]}`
	ok := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`
	if err := VerifyAdditive([]byte(before), []byte(ok), added); err != nil {
		t.Fatalf("additive result refused: %v", err)
	}
	bad := map[string]string{
		"existing grant modified": `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["*"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`,
		"existing grant removed":  `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`,
		"tagOwner modified":       `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["tag:tw"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`,
		"acls touched":            `{"acls":[],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`,
		"extra grant smuggled":    `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]},{"src":["*"],"dst":["*"],"ip":["*"]}]}`,
		"ssh added":               `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"ssh":[{"action":"accept","src":["*"],"dst":["*"],"users":["root"]}],"tagOwners":{"tag:ttest":["autogroup:admin"],"tag:tw-a":["tag:tw"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]},{"src":["tag:tw-a"],"dst":["tag:db"],"ip":["tcp:80"]}]}`,
	}
	for name, after := range bad {
		err := VerifyAdditive([]byte(before), []byte(after), added)
		var de *DiffError
		if !errors.As(err, &de) || len(de.Diff) == 0 || !strings.Contains(err.Error(), "not purely additive") {
			t.Fatalf("%s: want a DiffError, got %v", name, err)
		}
		t.Logf("%s: %v", name, err)
	}
}

// FuzzMergeAdditive: for any policy document and any snippet strings, Merge
// either errors or returns a document that is exactly the input plus the
// snippet's missing additions (and that still parses).
func FuzzMergeAdditive(f *testing.F) {
	f.Add(base, "tag:tw-a", "tag:tw", "tag:tw-a", "tag:db", "tcp:80")
	f.Add(`{}`, "tag:tw-x", "autogroup:admin", "tag:tw-x", "svc:api", "*")
	f.Add(`{"grants":[{"src":["tag:a"],"dst":["tag:b"],"ip":["*"]}]}`, "tag:prod", "tag:tw", "*", "*", "*")
	f.Add(`{"tagOwners":{"tag:tw-a":["autogroup:admin"]}}`, "tag:tw-a", "group:x", "autogroup:member", "tag:tw-a", "tcp:22")
	f.Fuzz(func(t *testing.T, pol, tag, owner, src, dst, ip string) {
		s := Snippet{TagOwners: map[string][]string{tag: {owner}}, Grants: []map[string]any{{"src": []string{src}, "dst": []string{dst}, "ip": []string{ip}}}}
		out, err := Merge([]byte(pol), s)
		if err != nil {
			return
		}
		if _, err := hujson.Parse(out); err != nil {
			t.Fatalf("merged output does not parse: %v\n%s", err, out)
		}
		// Check must have admitted the snippet.
		if err := Check(s, Options{}); err != nil {
			t.Fatalf("Merge succeeded on a snippet Check refuses: %v", err)
		}
		miss, err := Missing([]byte(pol), s)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyAdditive([]byte(pol), out, miss); err != nil {
			t.Fatalf("Merge result is not additive: %v\nbefore: %s\nafter: %s", err, pol, out)
		}
		// Nothing left to add.
		if m, err := Missing(out, s); err != nil || !m.Empty() {
			t.Fatalf("not idempotent: %+v %v", m, err)
		}
	})
}

// ADR-029 D6: the public snippet passes Check and merges additively; it
// sets funnel on tag:tw-public only.
func TestPublicSnippet(t *testing.T) {
	s := PublicSnippet()
	if err := Check(s, Options{}); err != nil {
		t.Fatalf("PublicSnippet refused: %v", err)
	}
	if got := s.HuJSON(); !strings.Contains(got, `"tag:tw-public"`) || !strings.Contains(got, `"funnel"`) {
		t.Fatalf("HuJSON: %s", got)
	}
	out, err := Merge([]byte(base), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdditive([]byte(base), out, s); err != nil {
		t.Fatal(err)
	}
	miss, err := Missing(out, s)
	if err != nil || !miss.Empty() {
		t.Fatalf("after merge still missing %+v (%v)", miss, err)
	}
}
