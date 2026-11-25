package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// hasUsage reports whether out (`tw <c> --help`, `tw help --all`) has c's
// usage: the whole line, or for a command with subcommands each
// alternative on its own "tw <c> …" line.
func hasUsage(out string, c command) bool {
	if strings.Contains(out, "usage: tw "+c.usage+"\n") || strings.Contains(out, "tw "+c.usage+"\n") {
		return true
	}
	if len(subcommandWords(c)) == 0 {
		return false
	}
	alts, common := usageAlts(c)
	for _, a := range alts {
		if !strings.Contains(out, "tw "+a+"\n") {
			return false
		}
	}
	return common == "" || strings.Contains(out, "common flags: "+common+"\n")
}

// TestSubcommandHelp: a dispatching command's --help lists its
// subcommands one per line (issue #438), and its bare run names them.
func TestSubcommandHelp(t *testing.T) {
	for name, want := range map[string][]string{
		"tenant":    {"add", "ls", "set", "rm"},
		"host":      {"add", "ls", "rm", "cordon", "uncordon", "drain", "setup", "vmcheck"},
		"d1":        {"create", "ls", "rm", "execute", "export", "repair", "migrations create|ls|apply"},
		"kv":        {"namespace create|ls|rm", "key put|get|rm|ls"},
		"image":     {"public add|ls|rm", "push"},
		"r2":        {"bucket ls|create|rm", "object ls"},
		"queues":    {"create", "ls", "info", "rm", "purge", "messages", "send"},
		"upgrade":   {"fleet|status|pause|resume|abort", "import", "releases", "rollback"},
		"telemetry": {"set", "rm"},
		"expose":    {"on|off", "names", "release"},
		"custody":   {"migrate"},
		"up":        nil,
		"deploy":    nil,
	} {
		var c command
		for _, cc := range commands {
			if cc.name == name {
				c = cc
			}
		}
		if got := subcommandWords(c); !slices.Equal(got, want) {
			t.Errorf("%s: subcommands %q, want %q", name, got, want)
		}
	}

	var so, se bytes.Buffer
	if code := run([]string{"tenant", "--help"}, &so, &se); code != 0 {
		t.Fatalf("tw tenant --help: exit %d", code)
	}
	for _, want := range []string{
		"subcommands:\n  tw tenant add <tailnet> [--oauth-client-file <file>]",
		"\n  tw tenant ls\n",
		"\n  tw tenant set <tenant> [--<setting> <value>]…  (tw tenant set --help; docs/limits.md)\n",
		"tw tenant <subcommand> --help   its flags",
	} {
		if !strings.Contains(so.String(), want) {
			t.Errorf("tw tenant --help lacks %q:\n%s", want, so.String())
		}
	}
	so.Reset()
	if code := run([]string{"tenant", "set", "--help"}, &so, &se); code != 0 || !strings.Contains(so.String(), "-max-objects") {
		t.Errorf("tw tenant set --help: exit %d, no --max-objects:\n%s", code, so.String())
	}
	so.Reset()
	if code := run([]string{"d1", "--help"}, &so, &se); code != 0 {
		t.Fatalf("tw d1 --help: exit %d", code)
	}
	for _, want := range []string{
		"\n  tw d1 execute <db> [--command <sql> | --file <file>]\n",
		"\n  tw d1 migrations apply <db>\n  common flags: [--workload <workload>] [--tenant <tailnet>]\n",
	} {
		if !strings.Contains(so.String(), want) {
			t.Errorf("tw d1 --help lacks %q:\n%s", want, so.String())
		}
	}

	if err := cmdHost(nil); err == nil || !strings.Contains(err.Error(), "tw host needs a subcommand: add, ls, rm, cordon, uncordon, drain, setup, vmcheck") {
		t.Errorf("tw host: %v", err)
	}
}
