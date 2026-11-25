package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	twagent "github.com/rosskukulinski/tailworks/agent"
	repodocs "github.com/rosskukulinski/tailworks/docs"
	"github.com/rosskukulinski/tailworks/e2e/agenteval"
)

// TestSkillCommandsExist fails when a skill (agent/skills) tells an agent
// to run a tailworks command or flag that doesn't exist: every `tw …` in a code
// block or inline code span is run as `tw … --help`, which parses every
// flag on the line (an unknown one is bad_request) and stops at --help
// before doing anything.
func TestSkillCommandsExist(t *testing.T) {
	skills := twagent.Skills()
	if len(skills) < 8 {
		t.Fatalf("only %d skills embedded", len(skills))
	}
	n := 0
	for _, s := range skills {
		cmds, _, err := twagent.Commands(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cmds {
			if len(c.Args) == 0 || strings.HasPrefix(c.Args[0], "<") {
				continue // cd, `tw`, `tw <command> --help`
			}
			n++
			if err := checkCLICommand(c.Args); err != nil {
				t.Errorf("%s:%d: %q: %v", c.Skill, c.Line, c.Text, err)
			}
		}
	}
	// The check itself must catch what goes stale.
	for _, bad := range [][]string{{"deploy", "--no-such-flag"}, {"no-such-command"}, {"logs", "x", "--follow"}, {"agent", "setup", "--scope"}} {
		if checkCLICommand(bad) == nil {
			t.Errorf("checkCLICommand(%q) passed; it must fail", bad)
		}
	}
	if n < 40 {
		t.Fatalf("checked only %d tw commands in the skills", n)
	}
	t.Logf("%d tw commands in %d skills checked", n, len(skills))
}

// TestSkillsWellFormed: front matter every agent accepts (Claude Code
// skill names, plain YAML scalars), cross-references to real skills,
// `tw docs show` refs that resolve, and the eval steps the eval test
// replays.
func TestSkillsWellFormed(t *testing.T) {
	t.Parallel()
	nameRE := regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	names := map[string]bool{}
	for _, s := range twagent.Skills() {
		names[s.Name] = true
	}
	steps := map[string]string{}
	for _, s := range twagent.Skills() {
		src, _ := twagent.Source(s.Name)
		if !strings.HasPrefix(string(src), "---\nname: "+s.Name+"\n") {
			t.Errorf("%s: SKILL.md must start with front matter naming its directory", s.Name)
		}
		if !nameRE.MatchString(s.Name) || (s.Name != twagent.CoreSkill && !strings.HasPrefix(s.Name, "tw-")) {
			t.Errorf("%s: skill names are tw or tw-<lowercase>", s.Name)
		}
		if d := s.Description; len(d) > 1024 || strings.Contains(d, ": ") || strings.ContainsAny(d[:1], `"'[{>|*&!%@`) || !strings.HasPrefix(d, "Use when") {
			t.Errorf("%s: description must be a plain YAML scalar starting \"Use when\", ≤1024 chars, no \": \": %q", s.Name, d)
		}
		for _, m := range regexp.MustCompile("`(tw-[a-z-]+)`").FindAllStringSubmatch(s.Body, -1) {
			if !names[m[1]] && !strings.HasPrefix(m[1], "tw-wl-") {
				t.Errorf("%s: refers to skill %s, which doesn't exist", s.Name, m[1])
			}
		}
		cmds, st, err := twagent.Commands(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cmds {
			if len(c.Args) >= 3 && c.Args[0] == "docs" && c.Args[1] == "show" && !strings.Contains(c.Args[2], "<") {
				if _, ok := repodocs.Show(c.Args[2]); !ok {
					t.Errorf("%s:%d: tw docs show %s: no such doc or section", c.Skill, c.Line, c.Args[2])
				}
			}
			if len(c.Args) >= 3 && c.Args[0] == "agent" && c.Args[1] == "show" && !strings.Contains(c.Args[2], "<") && !names[c.Args[2]] {
				t.Errorf("%s:%d: tw agent show %s: no such skill", c.Skill, c.Line, c.Args[2])
			}
		}
		for _, e := range st {
			if prev, dup := steps[e.Name]; dup {
				t.Errorf("eval step %q in %s and %s", e.Name, prev, s.Name)
			}
			steps[e.Name] = s.Name
			for _, c := range e.Commands {
				if strings.Contains(c.Text, "<") {
					t.Errorf("%s:%d: eval step %q has a placeholder: %q", c.Skill, c.Line, e.Name, c.Text)
				}
			}
		}
	}
	for _, want := range agenteval.Order {
		if steps[want] == "" {
			t.Errorf("no skill has the eval step %q (<!-- tw-eval: %s -->)", want, want)
		}
	}
	for got := range steps {
		if !contains(agenteval.Order, got) {
			t.Errorf("eval step %q isn't replayed by TestAgentEval (agenteval.Order)", got)
		}
	}
}

// checkCLICommand checks args (the words after "tw") name a real command
// and only flags it has.
func checkCLICommand(args []string) error {
	name := args[0]
	if name == "help" { // run()'s builtin: `tw help [<command>]`
		return nil
	}
	var cmd *command
	for i := range commands {
		if commands[i].name == name && !commands[i].hidden {
			cmd = &commands[i]
		}
	}
	if cmd == nil {
		return fmt.Errorf("no command %q", name)
	}
	if len(args) == 1 {
		return nil // a bare command name (`tw secret`): it exists
	}
	err := cmd.run(append(append([]string{}, args[1:]...), "--help"))
	var hr *helpRequest
	if !errors.As(err, &hr) {
		return fmt.Errorf("want the command to accept these flags, got %v", err)
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
