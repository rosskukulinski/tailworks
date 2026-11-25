package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppTags(t *testing.T) {
	t.Parallel()
	in := `{
  "name": "agent",
  "tailnet": {
    "tags": ["tag:tw-wl-agent"],
    "connects": ["tag:db"],
    "objects": {
      // a comment survives
      "SupportAgent": { "tags": ["tag:tw-agent-support"], "agent": true },
      "LookupAgent":  { "tags": ["tag:tw-agent-support-sub"] },
      "Mine":         { "tags": ["tag:custom"] }
    }
  }
}`
	out, ren, err := appTags([]byte(in), "billing")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"tags": ["tag:tw-wl-billing"]`,
		`"SupportAgent": { "tags": ["tag:tw-billing-support-agent"]`,
		`"LookupAgent":  { "tags": ["tag:tw-billing-lookup-agent"]`,
		`"tags": ["tag:custom"]`, // not a template tag: kept
		`"connects": ["tag:db"]`, // dependencies are never renamed
		"// a comment survives",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if len(ren) != 3 {
		t.Errorf("renamed %v, want 3 tags", ren)
	}
	if strings.Contains(s, "tag:tw-agent-support") {
		t.Errorf("old tag left in\n%s", s)
	}
}

func TestClassLabel(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"SupportAgent": "support-agent", "MCPServer": "mcpserver", "budget": "budget", "AiAgent": "ai-agent"} {
		if got := classLabel(in); got != want {
			t.Errorf("classLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInitNamesPackage: tw init names the npm package after the app (not
// "tw-hello") and gives every template an `npm run dev`.
func TestInitNamesPackage(t *testing.T) {
	if _, err := createTailworksDir(); err != nil {
		t.Skip("create-tailworks not found:", err)
	}
	for _, tmpl := range []string{"hello", "agent", "node-workflow"} {
		dir := filepath.Join(t.TempDir(), "my-app")
		if err := cmdInit([]string{dir, "--template", tmpl}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var pkg struct {
			Name    string            `json:"name"`
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(b, &pkg); err != nil {
			t.Fatalf("%s: package.json: %v", tmpl, err)
		}
		if pkg.Name != "my-app" || pkg.Scripts["dev"] != "tw dev" {
			t.Errorf("%s: package.json name %q, scripts %v", tmpl, pkg.Name, pkg.Scripts)
		}
	}
}

// TestWranglerQueuesNotWarned: wrangler's queues section works on tailworks
// (producers and consumers), so it must not be warned as "no such binding".
func TestWranglerQueuesNotWarned(t *testing.T) {
	t.Parallel()
	v := map[string]any{"producers": []any{map[string]any{"binding": "Q", "queue": "jobs"}}}
	for _, b := range []string{"esbuild", "wrangler"} {
		if n := wranglerKeyNote("queues", v, b); n != "" {
			t.Errorf("queues (%s): %q", b, n)
		}
	}
}
