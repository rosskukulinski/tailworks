package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every agent tailworks sets up skills for also gets a way to register the
// MCP server (issue #467), detected agents first.
func TestMCPRegistrations(t *testing.T) {
	det := Detect(Env{Home: t.TempDir()})
	for i := range det {
		det[i].Detected = det[i].ID == "gemini"
	}
	regs := MCPRegistrations([]string{"tw", "mcp"}, det)
	if len(regs) != len(Agents) {
		t.Fatalf("%d registrations for %d agents", len(regs), len(Agents))
	}
	if regs[0].ID != "gemini" || !regs[0].Detected {
		t.Fatalf("the detected agent should come first: %+v", regs[0])
	}
	want := map[string]string{
		"claude": "claude mcp add -s user tw -- tw mcp",
		"codex":  "codex mcp add tw -- tw mcp",
		"gemini": "gemini mcp add -s user tw tw mcp",
	}
	seen := map[string]bool{}
	for _, r := range regs {
		seen[r.ID] = true
		if w, ok := want[r.ID]; ok {
			if r.Command != w {
				t.Errorf("%s: %q, want %q", r.ID, r.Command, w)
			}
			continue
		}
		if r.Command != "" || r.File == "" || !json.Valid([]byte(r.Config)) {
			t.Errorf("%s: want a config file and valid JSON: %+v", r.ID, r)
		}
	}
	for _, a := range Agents {
		if !seen[a.ID] {
			t.Errorf("no MCP registration for %s", a.ID)
		}
	}

	// The operator's --allow is passed through to every agent.
	for _, r := range MCPRegistrations([]string{"tw", "mcp", "--allow", "kill"}, nil) {
		if !strings.Contains(r.Command+r.Config, "kill") {
			t.Errorf("%s drops --allow: %+v", r.ID, r)
		}
		if r.ID == "gemini" && r.Command != "gemini mcp add -s user tw tw mcp -- --allow kill" {
			t.Errorf("gemini: %q", r.Command)
		}
	}
}
