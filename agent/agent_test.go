package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func plan(t *testing.T, home string, o Options) *Plan {
	t.Helper()
	if o.Scope == "" {
		o.Scope = ScopeUser
	}
	if o.Version == "" {
		o.Version = "v-test"
	}
	p, err := MakePlan(Env{Home: home}, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func actions(p *Plan) map[string]Action {
	out := map[string]Action{}
	for _, c := range p.Changes {
		out[c.Path] = c.Action
	}
	return out
}

func TestSkillsParse(t *testing.T) {
	ss := Skills()
	if len(ss) == 0 || ss[0].Name != CoreSkill {
		t.Fatalf("skills: core skill first, got %v", ss)
	}
	for _, s := range ss {
		if s.Body == "" || strings.HasPrefix(s.Body, "---") {
			t.Errorf("%s: body %q", s.Name, s.Body[:min(40, len(s.Body))])
		}
	}
	if _, err := ParseSkill([]byte("no front matter")); err == nil {
		t.Fatal("ParseSkill accepted a file without front matter")
	}
}

// A newer tailworks that dropped a skill removes the old copy, but only if tailworks
// wrote it; a user's file in the way is skipped, never overwritten.
func TestPlanStaleAndForeign(t *testing.T) {
	home := t.TempDir()
	claude, _ := AgentByID("claude")
	skills := filepath.Join(home, ".claude", "skills")
	os.MkdirAll(filepath.Join(skills, "tw-gone"), 0o755)
	os.WriteFile(filepath.Join(skills, "tw-gone", "SKILL.md"), []byte(marker("v-old")+"\n"), 0o644)
	os.MkdirAll(filepath.Join(skills, "tw-deploy"), 0o755)
	os.WriteFile(filepath.Join(skills, "tw-deploy", "SKILL.md"), []byte("the user's own\n"), 0o644)
	p := plan(t, home, Options{Agents: []Agent{claude}})
	a := actions(p)
	if a[filepath.Join(skills, "tw-gone", "SKILL.md")] != Remove {
		t.Errorf("stale tw skill: %v", a)
	}
	if a[filepath.Join(skills, "tw-deploy", "SKILL.md")] != Skip {
		t.Errorf("user's file: %v", a)
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(skills, "tw-deploy", "SKILL.md")); string(b) != "the user's own\n" {
		t.Fatal("setup overwrote a file tailworks didn't write")
	}
	if _, err := os.Stat(filepath.Join(skills, "tw-gone")); !os.IsNotExist(err) {
		t.Fatal("stale skill directory left behind")
	}
	// Remove leaves the user's file too.
	p = plan(t, home, Options{Agents: []Agent{claude}, Remove: true})
	if _, ok := actions(p)[filepath.Join(skills, "tw-deploy", "SKILL.md")]; ok {
		t.Fatal("remove plans to touch the user's file")
	}
}

// The managed block is replaced in place on upgrade, keeping what's around
// it; a block with no end marker is refused, not guessed at.
func TestPlanBlock(t *testing.T) {
	home := t.TempDir()
	codex, _ := AgentByID("codex")
	f := filepath.Join(home, ".codex", "AGENTS.md")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("# top\n\n"+blockBegin+"\nold\n"+blockEnd+"\n\n# bottom\n"), 0o644)
	p := plan(t, home, Options{Agents: []Agent{codex}})
	if actions(p)[f] != Update {
		t.Fatalf("plan: %v", actions(p))
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	if !strings.HasPrefix(s, "# top\n\n"+blockBegin) || !strings.HasSuffix(s, blockEnd+"\n\n# bottom\n") || strings.Contains(s, "\nold\n") || strings.Count(s, blockBegin) != 1 {
		t.Fatalf("block not replaced in place:\n%s", s)
	}
	if actions(plan(t, home, Options{Agents: []Agent{codex}}))[f] != Unchanged {
		t.Fatal("second setup is not a no-op")
	}
	if err := Apply(plan(t, home, Options{Agents: []Agent{codex}, Remove: true})); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "# top\n\n# bottom\n" {
		t.Fatalf("after remove: %q", b)
	}
	os.WriteFile(f, []byte(blockBegin+"\nno end\n"), 0o644)
	if _, err := MakePlan(Env{Home: home}, Options{Scope: ScopeUser, Agents: []Agent{codex}, Version: "v"}); err == nil {
		t.Fatal("a block without its end marker was accepted")
	}
}

func TestCommandsParse(t *testing.T) {
	s := Skill{Name: "x", Body: "Run `tw ls --json` first.\n\n<!-- tw-eval: demo -->\n```sh\ntw init a --template mcp   # comment\ncd a && tw deploy --json\nprintf %s \"$K\" | tw secret put K\n```\n"}
	cmds, steps, err := Commands(s)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cmds {
		if c.Cd != "" {
			got = append(got, "cd "+c.Cd)
		} else {
			got = append(got, strings.Join(c.Args, " "))
		}
	}
	want := "ls --json|init a --template mcp|cd a|deploy --json|secret put K"
	if strings.Join(got, "|") != want {
		t.Fatalf("commands %q, want %q", strings.Join(got, "|"), want)
	}
	if len(steps) != 1 || steps[0].Name != "demo" || len(steps[0].Commands) != 4 {
		t.Fatalf("steps %+v", steps)
	}
}
