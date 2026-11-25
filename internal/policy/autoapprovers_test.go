package policy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func svcSnippet(name string, tags ...string) Snippet {
	return Snippet{AutoApprovers: AutoApprovers{Services: map[string][]string{name: tags}}}
}

// Issue #295: autoApprovers.services merges additively, whether the
// policy has no autoApprovers, only routes, or the Service already with
// another approver; routes and the other approver are kept, and a second
// merge adds nothing.
func TestMergeAutoApprovers(t *testing.T) {
	cases := map[string]struct{ policy, want string }{
		"no autoApprovers": {base, `"svc:support": ["tag:tw-wl-support"]`},
		"routes only": {`{
	"autoApprovers": {"routes": {"10.0.0.0/8": ["tag:router"]}}, // keep me
}`, `"svc:support": ["tag:tw-wl-support"]`},
		"other service":                 {`{"autoApprovers": {"services": {"svc:other": ["tag:other"]}}}`, `"svc:support": ["tag:tw-wl-support"]`},
		"service with another approver": {`{"autoApprovers": {"services": {"svc:support": ["tag:old"]}}}`, `"svc:support": ["tag:old", "tag:tw-wl-support"]`},
	}
	s := svcSnippet("svc:support", "tag:tw-wl-support")
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := Merge([]byte(tc.policy), s)
			if err != nil {
				t.Fatal(err)
			}
			std, _ := hujson.Standardize(append([]byte(nil), out...))
			compact := strings.Join(strings.Fields(string(std)), " ")
			if !strings.Contains(compact, tc.want) {
				t.Fatalf("merged policy lacks %s:\n%s", tc.want, out)
			}
			for _, keep := range []string{"// keep me", "tag:router", "svc:other", "tag:old", "// my tailnet"} {
				if strings.Contains(tc.policy, keep) && !strings.Contains(string(out), keep) {
					t.Fatalf("lost %q:\n%s", keep, out)
				}
			}
			miss, err := Missing([]byte(tc.policy), s)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAdditive([]byte(tc.policy), out, miss); err != nil {
				t.Fatal(err)
			}
			if m, err := Missing(out, s); err != nil || !m.Empty() {
				t.Fatalf("not idempotent: %+v %v", m, err)
			}
		})
	}
}

// RT-06 for autoApprovers: only svc: names, only tailworks tags as
// approvers, never on Headscale; refused before anything is written.
func TestAutoApproversRefused(t *testing.T) {
	cases := map[string]Snippet{
		"route key":           {AutoApprovers: AutoApprovers{Services: map[string][]string{"10.0.0.0/8": {"tag:tw-wl-x"}}}},
		"tag key":             svcSnippet("tag:tw-wl-x", "tag:tw-wl-x"),
		"bad svc name":        svcSnippet("svc:Not_A_Label", "tag:tw-wl-x"),
		"empty svc name":      svcSnippet("svc:", "tag:tw-wl-x"),
		"no approvers":        svcSnippet("svc:x"),
		"approver *":          svcSnippet("svc:x", "*"),
		"approver admin":      svcSnippet("svc:x", "autogroup:admin"),
		"approver member":     svcSnippet("svc:x", "autogroup:member"),
		"approver group":      svcSnippet("svc:x", "group:eng"),
		"approver user":       svcSnippet("svc:x", "alice@example.com"),
		"approver non-tw tag": svcSnippet("svc:x", "tag:prod"),
		"approver mixed":      svcSnippet("svc:x", "tag:tw-wl-x", "tag:prod"),
		"approver prefix":     svcSnippet("svc:x", "tag:twprod"),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check(s, Options{}); err == nil {
				t.Fatalf("Check accepted %+v", s)
			}
			if out, err := Merge([]byte(base), s); err == nil {
				t.Fatalf("Merge accepted it:\n%s", out)
			}
			f := &fakeAPI{body: []byte(base), etag: `"e1"`}
			if _, err := Apply(t.Context(), f, s); err == nil || f.sets != 0 || string(f.body) != base {
				t.Fatalf("Apply wrote it: err=%v sets=%d", err, f.sets)
			}
		})
	}
	good := svcSnippet("svc:support", "tag:tw-wl-support")
	if err := Check(good, Options{}); err != nil {
		t.Fatalf("legitimate entry refused: %v", err)
	}
	if err := Check(good, Options{Headscale: true}); err == nil {
		t.Fatal("the Headscale profile accepted autoApprovers.services")
	}
}

// VerifyAdditive: routes, exitNode and existing services entries can't
// change, and no services entry may be smuggled in.
func TestVerifyAdditiveAutoApprovers(t *testing.T) {
	added := svcSnippet("svc:a", "tag:tw-a")
	before := `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:b":["tag:b"]}}}`
	ok := `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:b":["tag:b"],"svc:a":["tag:tw-a"]}}}`
	if err := VerifyAdditive([]byte(before), []byte(ok), added); err != nil {
		t.Fatalf("additive result refused: %v", err)
	}
	bad := map[string]string{
		"routes modified":   `{"autoApprovers":{"routes":{"0.0.0.0/0":["tag:r"]},"services":{"svc:b":["tag:b"],"svc:a":["tag:tw-a"]}}}`,
		"exitNode added":    `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"exitNode":["tag:tw-a"],"services":{"svc:b":["tag:b"],"svc:a":["tag:tw-a"]}}}`,
		"service modified":  `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:b":["tag:tw-a"],"svc:a":["tag:tw-a"]}}}`,
		"service removed":   `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:a":["tag:tw-a"]}}}`,
		"service smuggled":  `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:b":["tag:b"],"svc:a":["tag:tw-a"],"svc:c":["tag:tw-a"]}}}`,
		"approver smuggled": `{"autoApprovers":{"routes":{"10.0.0.0/8":["tag:r"]},"services":{"svc:b":["tag:b"],"svc:a":["tag:tw-a","tag:prod"]}}}`,
		"autoApprovers cut": `{}`,
	}
	for name, after := range bad {
		err := VerifyAdditive([]byte(before), []byte(after), added)
		var de *DiffError
		if !errors.As(err, &de) || len(de.Diff) == 0 {
			t.Fatalf("%s: want a DiffError, got %v", name, err)
		}
	}
}

func TestAutoApproversHuJSON(t *testing.T) {
	s := svcSnippet("svc:support", "tag:tw-wl-support")
	h := s.HuJSON()
	var back Snippet
	std, err := hujson.Standardize([]byte("{" + h + "}"))
	if err != nil {
		t.Fatalf("HuJSON doesn't parse: %v\n%s", err, h)
	}
	if err := json.Unmarshal(std, &back); err != nil || len(back.AutoApprovers.Services["svc:support"]) != 1 {
		t.Fatalf("round trip %+v %v\n%s", back, err, h)
	}
	if s.Empty() {
		t.Fatal("a snippet with only autoApprovers is empty")
	}
	for _, l := range s.Diff() {
		if !strings.HasPrefix(l, "+ ") {
			t.Fatalf("diff line %q", l)
		}
	}
	if Headscale(s).AutoApprovers.Services != nil {
		t.Fatal("the Headscale profile kept autoApprovers.services")
	}
}
