package policy

import (
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// TestRender (#535): one renderer, grants in src, dst, ip, app order with
// spaces, per-grant notes, the merge comment first, and every form valid
// HuJSON once wrapped in braces.
func TestRender(t *testing.T) {
	s := Snippet{
		TagOwners: map[string][]string{"tag:tw-wl-b": {"autogroup:admin", "tag:tw"}, "tag:tw-wl-a": {"tag:tw"}},
		Grants: []map[string]any{
			{"ip": []string{"tcp:5432"}, "dst": []string{"tag:db"}, "src": []string{"tag:tw-wl-a", "tag:tw-wl-b"}},
			{"app": map[string]any{"example.com/cap/x": []any{map[string]any{}}}, "src": []any{"tag:tw-wl-a"}, "dst": []any{"tag:tw-wl-b"}, "ip": []any{"tcp:80"}},
		},
		NodeAttrs:     []map[string]any{{"attr": []string{"funnel"}, "target": []string{"tag:tw-public"}}},
		AutoApprovers: AutoApprovers{Services: map[string][]string{"svc:a": {"tag:tw-wl-a"}}},
	}
	if g, want := EntryHuJSON(s.Grants[0]), `{ "src": ["tag:tw-wl-a", "tag:tw-wl-b"], "dst": ["tag:db"], "ip": ["tcp:5432"] }`; g != want {
		t.Errorf("EntryHuJSON\n got %s\nwant %s", g, want)
	}
	if g, want := EntryHuJSON(s.Grants[1]), `{ "src": ["tag:tw-wl-a"], "dst": ["tag:tw-wl-b"], "ip": ["tcp:80"], "app": {"example.com/cap/x":[{}]} }`; g != want {
		t.Errorf("EntryHuJSON with app\n got %s\nwant %s", g, want)
	}
	if g, want := EntryHuJSON(s.NodeAttrs[0]), `{ "target": ["tag:tw-public"], "attr": ["funnel"] }`; g != want {
		t.Errorf("nodeAttrs entry\n got %s\nwant %s", g, want)
	}
	in := s.Inline()
	for _, want := range []string{
		`"tagOwners": { "tag:tw-wl-a": ["tag:tw"], "tag:tw-wl-b": ["autogroup:admin", "tag:tw"] }`,
		`"autoApprovers": { "services": { "svc:a": ["tag:tw-wl-a"] } }`,
	} {
		if !strings.Contains(in, want) {
			t.Errorf("Inline lacks %s:\n%s", want, in)
		}
	}
	if strings.Contains(in, "//") {
		t.Errorf("Inline has a comment:\n%s", in)
	}
	p := s.Paste([]string{"tailnet.connects"})
	if !strings.HasPrefix(p, MergeComment+"\n") || !strings.Contains(p, `"ip": ["tcp:5432"] }, // tailnet.connects`) {
		t.Errorf("Paste:\n%s", p)
	}
	for name, h := range map[string]string{"Inline": in, "HuJSON": s.HuJSON(), "Paste": p} {
		std, err := hujson.Standardize([]byte("{" + h + "}"))
		if err != nil {
			t.Fatalf("%s is not HuJSON: %v\n%s", name, err, h)
		}
		got, err := Missing(std, s)
		if err != nil || !got.Empty() {
			t.Errorf("%s doesn't round-trip: missing %+v (%v)", name, got, err)
		}
	}
	if (Snippet{}).Paste(nil) != "" {
		t.Error("Paste of an empty snippet")
	}
}
