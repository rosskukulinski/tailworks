package main

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/hujson"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// The policy snippets in the docs are what admins copy into their tailnet
// policy, so they must say exactly what the code writes and checks. They
// drifted once (tag:tw-issuer in the OAuth one). Each doc below names the
// snippets it carries and how many times; a jsonc or hujson block with a
// "tagOwners" key is matched to one of them by its set of tagOwners keys
// and must equal it (tagOwners, grants and nodeAttrs). Blocks whose key set
// matches none of a doc's snippets (an app's own tags, for example) aren't
// checked here.
func TestDocPolicySnippetsMatchCode(t *testing.T) {
	t.Parallel()
	// The README also has the one line to paste before tw up --apply-policy
	// writes the rest (issue #320): tag:tw alone, as operatorSnippet has it.
	twOnly := policy.Snippet{TagOwners: map[string][]string{"tag:tw": operatorSnippet().TagOwners["tag:tw"]}}
	type want struct {
		name string
		s    policy.Snippet
		n    int
	}
	for _, tc := range []struct {
		file  string
		wants []want
	}{
		{"../../README.md", []want{{"operatorSnippet", operatorSnippet(), 1}, {"the tag:tw line", twOnly, 1}}},
		// INSTALL.md links the README's snippet; a copy here must match too.
		{"../../docs/INSTALL.md", []want{{"operatorSnippet", operatorSnippet(), 0}, {"the tag:tw line", twOnly, 0}}},
		{"../../docs/headscale.md", []want{{"policy.Headscale(baseSnippet())", headscaleSnippet(), 1}}},
		{"../../docs/public-ingress.md", []want{{"policy.PublicSnippet", policy.PublicSnippet(), 1}}},
	} {
		b, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, body := range docPolicyBlocks(b) {
			std, err := hujson.Standardize([]byte("{" + body + "}"))
			if err != nil {
				t.Errorf("%s: snippet doesn't parse: %v\n%s", tc.file, err, body)
				continue
			}
			var doc policy.Snippet
			if err := json.Unmarshal(std, &doc); err != nil {
				t.Errorf("%s: snippet: %v\n%s", tc.file, err, body)
				continue
			}
			i := slices.IndexFunc(tc.wants, func(w want) bool { return sameKeys(w.s.TagOwners, doc.TagOwners) })
			if i < 0 {
				continue
			}
			w := tc.wants[i]
			got[w.name]++
			if !reflect.DeepEqual(doc.TagOwners, w.s.TagOwners) {
				t.Errorf("%s: tagOwners = %v, want %s's %v", tc.file, doc.TagOwners, w.name, w.s.TagOwners)
			}
			if a, b := normJSON(t, doc.Grants), normJSON(t, w.s.Grants); !reflect.DeepEqual(a, b) {
				t.Errorf("%s: grants = %v, want %s's %v", tc.file, a, w.name, b)
			}
			if a, b := normJSON(t, doc.NodeAttrs), normJSON(t, w.s.NodeAttrs); !reflect.DeepEqual(a, b) {
				t.Errorf("%s: nodeAttrs = %v, want %s's %v", tc.file, a, w.name, b)
			}
		}
		for _, w := range tc.wants {
			if got[w.name] != w.n {
				t.Errorf("%s: found %d copies of %s, want %d (update this test if you add or remove one)", tc.file, got[w.name], w.name, w.n)
			}
		}
	}
}

// docPolicyBlocks returns the bodies of the jsonc and hujson code blocks in
// a Markdown doc that have a "tagOwners" key.
func docPolicyBlocks(md []byte) []string {
	var out []string
	for _, m := range regexp.MustCompile("(?s)```(?:jsonc|hujson)\n(.*?)```").FindAllSubmatch(md, -1) {
		if body := string(m[1]); strings.Contains(body, `"tagOwners"`) {
			out = append(out, body)
		}
	}
	return out
}

func sameKeys(a, b map[string][]string) bool {
	return slices.Equal(slices.Sorted(maps.Keys(a)), slices.Sorted(maps.Keys(b)))
}

// normJSON round-trips v through JSON, so a snippet built in Go and one
// parsed from a doc compare equal; nil and empty both become nil.
func normJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if a, ok := out.([]any); ok && len(a) == 0 {
		return nil
	}
	return out
}
