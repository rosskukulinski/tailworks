package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Refresh (tw upgrade, issue #490) rewrites only what an older tw wrote:
// no new skill files or blocks, nothing about files tw didn't write.
func TestRefreshOnlyUpdatesStamped(t *testing.T) {
	home := t.TempDir()
	all := Agents
	if p := plan(t, home, Options{Agents: all, Refresh: true}); p.Stamped() || len(p.Changes) != 0 {
		t.Fatalf("empty home: %+v", p.Changes)
	}

	// An old tw wrote Claude's core skill and Codex's block; the user has
	// their own tw-deploy skill and a Gemini file without a tw block.
	skills := filepath.Join(home, ".claude", "skills")
	core := filepath.Join(skills, CoreSkill, "SKILL.md")
	os.MkdirAll(filepath.Dir(core), 0o755)
	os.WriteFile(core, []byte(marker("v-old")+"\nold\n"), 0o644)
	own := filepath.Join(skills, "tw-deploy", "SKILL.md")
	os.MkdirAll(filepath.Dir(own), 0o755)
	os.WriteFile(own, []byte("the user's own\n"), 0o644)
	codex := filepath.Join(home, ".codex", "AGENTS.md")
	os.MkdirAll(filepath.Dir(codex), 0o755)
	os.WriteFile(codex, []byte("mine\n\n"+renderBlock(Skills(), "v-old")), 0o644)
	gemini := filepath.Join(home, ".gemini", "GEMINI.md")
	os.MkdirAll(filepath.Dir(gemini), 0o755)
	os.WriteFile(gemini, []byte("mine\n"), 0o644)

	p := plan(t, home, Options{Agents: all, Refresh: true})
	if !p.Stamped() {
		t.Fatal("not stamped")
	}
	a := actions(p)
	if a[core] != Update || a[codex] != Update {
		t.Fatalf("stamped files: %v", a)
	}
	for path, act := range a {
		if act == Create || act == Skip {
			t.Errorf("%s: %s", path, act)
		}
	}
	if _, ok := a[gemini]; ok {
		t.Errorf("gemini (no tw block) is in the plan")
	}
	if _, ok := a[own]; ok {
		t.Errorf("the user's own skill is in the plan")
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(core); !strings.Contains(string(b), marker("v-test")) {
		t.Fatalf("core skill not refreshed: %q", b)
	}
	if b, _ := os.ReadFile(codex); !strings.HasPrefix(string(b), "mine\n") || !strings.Contains(string(b), marker("v-test")) {
		t.Fatalf("codex block not refreshed: %q", b)
	}
	if b, _ := os.ReadFile(gemini); string(b) != "mine\n" {
		t.Fatalf("gemini changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode")); err == nil {
		t.Fatal("refresh created opencode's file")
	}
}
