package policy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func TestHeadscaleProfileSnippet(t *testing.T) {
	s := InfraSnippet(7474)
	s.Grants = append(s.Grants, AdminGrant("autogroup:admin", "tag:tw-mgmt", "example.com/cap/admin"))
	s.NodeAttrs = PublicSnippet().NodeAttrs
	hs := Headscale(s)
	if err := Check(hs, Options{Headscale: true}); err != nil {
		t.Fatalf("Check(Headscale(infra)): %v", err)
	}
	if got := hs.TagOwners["tag:tw"]; len(got) != 0 {
		t.Errorf("tag:tw owners = %v, want none", got)
	}
	if got := hs.TagOwners["tag:tw-mgmt"]; len(got) != 1 || got[0] != "tag:tw" {
		t.Errorf("tag:tw-mgmt owners = %v, want [tag:tw]", got)
	}
	if len(hs.NodeAttrs) != 0 || len(hs.Grants) != 2 {
		t.Errorf("nodeAttrs %v grants %v: want none and the 2 infra grants", hs.NodeAttrs, hs.Grants)
	}
	// The input is untouched.
	if len(s.TagOwners["tag:tw"]) != 1 {
		t.Errorf("input modified: %v", s.TagOwners)
	}
	// Merged into an empty policy it is valid HuJSON with every tag defined.
	out, err := MergeWith([]byte("{}"), hs, Options{Headscale: true})
	if err != nil {
		t.Fatal(err)
	}
	std, err := hujson.Standardize(out)
	if err != nil {
		t.Fatalf("merged policy: %v\n%s", err, out)
	}
	var d struct {
		TagOwners map[string][]string `json:"tagOwners"`
	}
	if err := json.Unmarshal(std, &d); err != nil {
		t.Fatal(err)
	}
	if o, ok := d.TagOwners["tag:tw"]; !ok || len(o) != 0 {
		t.Errorf("merged tag:tw owners = %v (present %v), want []", o, ok)
	}
	miss, err := Missing(out, hs)
	if err != nil || !miss.Empty() {
		t.Errorf("Missing after merge = %+v, %v", miss, err)
	}
}

// TestHeadscaleProfileKeepsMemberGrant (#552): the caller grant's
// autogroup:member source survives the profile; autogroup:admin doesn't.
func TestHeadscaleProfileKeepsMemberGrant(t *testing.T) {
	member := map[string]any{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}
	admin := map[string]any{"src": []string{"autogroup:admin"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}
	mixed := map[string]any{"src": []string{"autogroup:member", "autogroup:admin"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}
	s := Snippet{TagOwners: map[string][]string{"tag:tw-wl-x": {"autogroup:admin", "tag:tw"}}, Grants: []map[string]any{member, admin, mixed}}
	hs := Headscale(s)
	if len(hs.Grants) != 1 || strs(hs.Grants[0]["src"])[0] != "autogroup:member" {
		t.Fatalf("grants = %v, want only the autogroup:member one", hs.Grants)
	}
	opts := Options{Headscale: true, AllowSrcs: []string{"autogroup:member"}}
	if err := Check(hs, opts); err != nil {
		t.Fatalf("Check(Headscale) with autogroup:member allowed: %v", err)
	}
	// RT-06 still holds: without AllowSrcs the source is refused.
	if err := Check(hs, Options{Headscale: true}); err == nil {
		t.Fatal("Check(Headscale) accepted autogroup:member without AllowSrcs")
	}
}

func TestHeadscaleProfileCheckRejects(t *testing.T) {
	cases := map[string]Snippet{
		"autogroup owner": {TagOwners: map[string][]string{"tag:tw-x": {"autogroup:admin"}}},
		"svc dst":         {Grants: []map[string]any{{"src": []string{"tag:tw"}, "dst": []string{"svc:x"}, "ip": []string{"tcp:443"}}}},
		"funnel":          PublicSnippet(),
		"autogroup src":   {Grants: []map[string]any{{"src": []string{"autogroup:admin"}, "dst": []string{"tag:tw-mgmt"}, "ip": []string{"tcp:443"}}}},
		"posture":         {Grants: []map[string]any{{"src": []string{"tag:tw"}, "dst": []string{"tag:tw"}, "ip": []string{"*"}, "srcPosture": []string{"posture:x"}}}},
	}
	for name, s := range cases {
		err := Check(s, Options{Headscale: true, AllowSrcs: []string{"autogroup:admin"}})
		if err == nil {
			t.Errorf("%s: Check accepted %+v", name, s)
		}
	}
	// autogroup:member is a grant source Headscale accepts; other
	// autogroups are refused even when allowed.
	for _, src := range []string{"autogroup:tagged", "autogroup:self"} {
		g := Snippet{Grants: []map[string]any{{"src": []string{src}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}}
		if err := Check(g, Options{Headscale: true, AllowSrcs: []string{src}}); err == nil {
			t.Errorf("Check(Headscale) accepted a %s source", src)
		}
	}
	// Without the profile, tag:tw still needs an owner.
	if err := Check(Snippet{TagOwners: map[string][]string{"tag:tw": {}}}, Options{}); err == nil || !strings.Contains(err.Error(), "no owners") {
		t.Errorf("tailscale profile accepted an ownerless tag:tw: %v", err)
	}
}
