package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agentTwConfig = `{
  "name": "agent",
  "tailnet": {
    "tags": ["tag:tw-wl-agent"],
    "connects": ["tag:db"],
    "objects": {
      // a comment survives
      "SupportAgent": { "tags": ["tag:tw-agent-support"], "agent": true },
      "LookupAgent":  { "tags": ["tag:tw-agent-support-sub"] },
      "Budget":       { "node": false }
    }
  }
}`

func mustOverrides(t *testing.T, tags string, objectTags ...string) initTagOverrides {
	t.Helper()
	o, err := parseInitTagOverrides(tags, objectTags)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSetTags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		tags       string
		objectTags []string
		want       []string
	}{
		{"workload only", "tag:tw-app", nil, []string{
			`"tags": ["tag:tw-app"],`,
			`"SupportAgent": { "tags": ["tag:tw-agent-support"]`,
			`"LookupAgent":  { "tags": ["tag:tw-agent-support-sub"]`,
		}},
		{"all classes", "tag:tw-app, tag:tw-internal", []string{"tag:tw-agent"}, []string{
			`"tags": ["tag:tw-app", "tag:tw-internal"],`,
			`"SupportAgent": { "tags": ["tag:tw-agent"]`,
			`"LookupAgent":  { "tags": ["tag:tw-agent"]`,
		}},
		{"one class beats all", "", []string{"tag:tw-agent", "LookupAgent=tag:tw-sub"}, []string{
			`"tags": ["tag:tw-wl-agent"],`,
			`"SupportAgent": { "tags": ["tag:tw-agent"]`,
			`"LookupAgent":  { "tags": ["tag:tw-sub"]`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := setTags([]byte(agentTwConfig), mustOverrides(t, tc.tags, tc.objectTags...))
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			for _, w := range append(tc.want, `"connects": ["tag:db"]`, "// a comment survives", `"Budget":       { "node": false }`) {
				if !strings.Contains(s, w) {
					t.Errorf("missing %s in\n%s", w, s)
				}
			}
		})
	}
}

func TestSetTagsRefuses(t *testing.T) {
	t.Parallel()
	shared := `{"tailnet": {"tags": ["tag:x"], "objects": {"A": {"tags": ["tag:x"]}}}}`
	for name, tc := range map[string]struct {
		cfg        string
		tags       string
		objectTags []string
		want       string
	}{
		"unknown class":      {agentTwConfig, "", []string{"Nope=tag:a"}, "no class Nope"},
		"class with no node": {agentTwConfig, "", []string{"Budget=tag:a"}, "no node of its own"},
		"no node classes":    {`{"tailnet": {"tags": ["tag:tw-wl-hello"]}}`, "", []string{"tag:a"}, "no Durable Object class"},
		"shared tag set":     {shared, "tag:y", nil, "same tags"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := setTags([]byte(tc.cfg), mustOverrides(t, tc.tags, tc.objectTags...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// Both nodes of a shared set set alike is fine.
	if _, err := setTags([]byte(shared), mustOverrides(t, "tag:y", "tag:y")); err != nil {
		t.Fatal(err)
	}
}

func TestParseInitTagOverridesRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tags       string
		objectTags []string
		code, want string
	}{
		{"db", nil, "bad_request", "not a valid tag"},
		{"tag:tw-host", nil, "reserved_tag", "tailworks' own tags"},
		{" , ", nil, "bad_request", "at least one tag"},
		{"", []string{"=tag:a"}, "bad_request", "no class"},
		{"", []string{"tag:a", "tag:b"}, "bad_request", "given twice"},
		{"", []string{"A=tag:a", "A=tag:b"}, "bad_request", "given twice"},
		{"", []string{"A=tag:tw"}, "reserved_tag", "tailworks' own tags"},
	} {
		_, err := parseInitTagOverrides(tc.tags, tc.objectTags)
		ce, ok := err.(*cliErr)
		if !ok || ce.Code != tc.code || !strings.Contains(ce.Error(), tc.want) {
			t.Errorf("%q %q: err = %v, want %s %q", tc.tags, tc.objectTags, err, tc.code, tc.want)
		}
	}
}

// TestInitExistingTags: tw init --tags/--object-tags writes the given tags
// into the new app, and refuses before writing anything when they don't fit
// the template.
func TestInitExistingTags(t *testing.T) {
	if _, err := createTailworksDir(); err != nil {
		t.Skip("create-tailworks not found:", err)
	}
	dir := filepath.Join(t.TempDir(), "billing")
	if err := cmdInit([]string{dir, "--template", "agent", "--tags", "tag:tw-wl-shared", "--object-tags", "tag:tw-agent", "--object-tags", "LookupAgent=tag:tw-agent-sub"}); err != nil {
		t.Fatal(err)
	}
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Cfg.Tailnet.Tags, ","); got != "tag:tw-wl-shared" {
		t.Errorf("tailnet.tags = %s (the tag:tw-wl-<workload> rename must not touch --tags)", got)
	}
	for c, want := range map[string]string{"SupportAgent": "tag:tw-agent", "LookupAgent": "tag:tw-agent-sub"} {
		if got := strings.Join(a.Cfg.Tailnet.Objects[c].Tags, ","); got != want {
			t.Errorf("%s tags = %s, want %s", c, got, want)
		}
	}

	bad := filepath.Join(t.TempDir(), "hello")
	if err := cmdInit([]string{bad, "--template", "hello", "--object-tags", "tag:tw-agent"}); err == nil {
		t.Fatal("--object-tags on a template with no object nodes: no error")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Errorf("a refused tw init left %s behind (%v)", bad, err)
	}
}
