package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// One rule for every subcommand that deletes data or a tenant's resources
// (issue #72):
//
//   - --yes goes ahead.
//   - On a terminal, without --json, it says what will be lost and asks for
//     the name to be typed back. Anything else cancels.
//   - Otherwise (a script, a coding agent, --json) it refuses with
//     confirmation_required, before contacting mgmt. The fix names --yes,
//     which an agent passes only after the user agreed to that action.
//
// The commands that use it are listed in destructiveCommands; a test checks
// that each one refuses without --yes.

// yesFlag registers --yes on a destructive subcommand's flag set.
func yesFlag(f *flags) *bool {
	return yesFlagBefore(f, "deleting") // helpflags.go
}

// confirmIn and confirmIsTerminal are the prompt's input; tests replace them.
var (
	confirmIn         io.Reader = os.Stdin
	confirmIsTerminal           = isTerminal
)

// confirmDestroy guards one destructive action. what says what is lost
// ("deleting queue jobs deletes its messages"); name is what the user types
// to confirm; cmdline is the command to re-run with --yes; fix lines
// (a backup, a drain) come first in the refusal.
func confirmDestroy(f *flags, yes bool, what, name, cmdline string, fix ...string) error {
	if yes {
		return nil
	}
	if *f.json || !confirmIsTerminal() {
		return fail("confirmation_required", what+"; there is no undo, so it asks first").
			withFix(append(fix, cmdline+" --yes")...)
	}
	fmt.Fprintf(os.Stderr, "%s; there is no undo.\nType %s to confirm: ", what, name)
	ln, _ := bufio.NewReader(confirmIn).ReadString('\n')
	if strings.TrimSpace(ln) != name {
		return fail("cancelled", "nothing was deleted")
	}
	return nil
}

// destructiveCommands is every subcommand guarded by confirmDestroy, as the
// arguments of a minimal invocation. TestDestructiveCommandsConfirm runs
// each without --yes and without a terminal.
var destructiveCommands = [][]string{
	{"d1", "delete", "db", "--workload", "app"}, // wrangler's spelling of rm (normalizeVerb)
	{"d1", "repair", "db", "--skip", "3", "--workload", "app"},
	{"d1", "rm", "db", "--workload", "app"},
	{"host", "rm", "h1"},
	{"kv", "namespace", "rm", "sessions", "--workload", "app"},
	{"queues", "purge", "jobs"},
	{"queues", "rm", "jobs"},
	{"r2", "bucket", "rm", "photos", "--workload", "app"},
	{"restore", "backup-dir"},
	{"tenant", "rm", "other.example.ts.net"},
	{"upgrade", "rollback", "--restore"},
}
