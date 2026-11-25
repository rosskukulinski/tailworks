package main

import (
	"strings"
	"testing"
)

// TestDestructiveCommandsConfirm: every subcommand in destructiveCommands
// refuses without --yes when there is no terminal (or with --json), before
// it opens the state or contacts mgmt; on a terminal it asks for the name,
// and anything but the name cancels (issue #72).
func TestDestructiveCommandsConfirm(t *testing.T) {
	cmd := func(name string) command {
		for _, c := range commands {
			if c.name == name {
				return c
			}
		}
		t.Fatalf("no command %s", name)
		return command{}
	}
	name := func(args []string) string { // the positional after the subcommand(s)
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && a != "delete" && a != "repair" && a != "rm" && a != "purge" && a != "namespace" && a != "bucket" {
				return a
			}
		}
		t.Fatalf("no name in %v", args)
		return ""
	}
	// No state dir is ever created: refusals come before openState.
	dir := t.TempDir() + "/never-created"
	runIt := func(args []string, terminal bool, input string) error {
		t.Helper()
		oldT, oldIn := confirmIsTerminal, confirmIn
		t.Cleanup(func() { confirmIsTerminal, confirmIn = oldT, oldIn })
		confirmIsTerminal = func() bool { return terminal }
		confirmIn = strings.NewReader(input)
		return cmd(args[0]).run(append(append([]string{}, args[1:]...), "--state-dir", dir))
	}

	for _, args := range destructiveCommands {
		line := "tw " + strings.Join(args, " ")
		// No terminal: refused, and the fix is the same command with --yes.
		err := runIt(args, false, "")
		if cliCode(err) != "confirmation_required" {
			t.Errorf("%s (no terminal): %v, want confirmation_required", line, err)
		} else if !strings.Contains(cliText(err), "--yes") {
			t.Errorf("%s: the refusal doesn't name --yes: %s", line, cliText(err))
		}
		// --json on a terminal: never prompts.
		if err := runIt(append(append([]string{}, args...), "--json"), true, name(args)+"\n"); cliCode(err) != "confirmation_required" {
			t.Errorf("%s --json (terminal): %v, want confirmation_required", line, err)
		}
		// Terminal, wrong answer: cancelled.
		if err := runIt(args, true, "y\n"); cliCode(err) != "cancelled" {
			t.Errorf("%s (terminal, answered y): %v, want cancelled", line, err)
		}
		// Terminal, the name typed back, or --yes: goes on (and stops later:
		// there is no tailworks here).
		for _, c := range []struct {
			args  []string
			input string
		}{{args, name(args) + "\n"}, {append(append([]string{}, args...), "--yes"), ""}} {
			if code := cliCode(runIt(c.args, true, c.input)); code == "confirmation_required" || code == "cancelled" {
				t.Errorf("%s (input %q): %s, want it to go on", "tw "+strings.Join(c.args, " "), c.input, code)
			}
		}
	}
}
