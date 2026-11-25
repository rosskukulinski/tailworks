package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelp: `tw --help`, `tw help`, `tw help <cmd>` and `tw <cmd> --help`
// (any spelling, any position, also after global flags and for subcommands)
// print usage to stdout and exit 0, never the bad_request error page.
func TestHelp(t *testing.T) {
	runOK := func(args ...string) string {
		t.Helper()
		var so, se bytes.Buffer
		if code := run(args, &so, &se); code != 0 {
			t.Fatalf("tw %s: exit %d, stdout %q stderr %q", strings.Join(args, " "), code, so.String(), se.String())
		}
		if se.Len() != 0 {
			t.Fatalf("tw %s: stderr %q", strings.Join(args, " "), se.String())
		}
		if strings.Contains(so.String(), "help requested") || strings.Contains(so.String(), "bad_request") {
			t.Fatalf("tw %s: help printed as an error:\n%s", strings.Join(args, " "), so.String())
		}
		return so.String()
	}
	for _, args := range [][]string{{}, {"--help"}, {"-h"}, {"help"}} {
		out := runOK(args...)
		for _, want := range []string{"usage: tw <command>", "hello (default) | agent | mcp | static-site", "tw init --list", "  init ", "  doctor ", "tw <command> --help"} {
			if !strings.Contains(out, want) {
				t.Errorf("tw %s: global help lacks %q:\n%s", strings.Join(args, " "), want, out)
			}
		}
		if strings.Contains(out, "__supervise") {
			t.Errorf("global help lists the hidden command:\n%s", out)
		}
	}
	for _, c := range commands {
		if c.hidden {
			continue
		}
		for _, args := range [][]string{{c.name, "--help"}, {c.name, "-h"}, {c.name, "-help"}, {"help", c.name}, {"--state-dir", "/nonexistent", c.name, "--help"}, {c.name, "--help", "extra", "--flags"}} {
			out := runOK(args...)
			if !hasUsage(out, c) || !strings.Contains(out, c.help) {
				t.Errorf("tw %s: want the %s usage line, got:\n%s", strings.Join(args, " "), c.name, out)
			}
		}
	}
	// Subcommands and the init template list.
	for _, args := range [][]string{{"host", "setup", "--help"}, {"tenant", "add", "-h"}, {"init", "x", "--template", "agent", "--help"}} {
		runOK(args...)
	}
	init := runOK("init", "--help")
	if !strings.Contains(init, "hello|agent|mcp|static-site]") || !strings.Contains(init, "init --list") || strings.Contains(init, "refund-approval") {
		t.Errorf("init help lacks the four starting points / --list, or lists the demos:\n%s", init)
	}
	// After `--` nothing is a help flag; a bare `tw` with only flags is a usage error.
	var so, se bytes.Buffer
	if code := run([]string{"--state-dir", "/nonexistent"}, &so, &se); code != 2 || !strings.Contains(se.String(), "usage: tw <command>") {
		t.Errorf("tw --state-dir X: exit %d, stderr %q", code, se.String())
	}
}
