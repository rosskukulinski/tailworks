package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// TestUnknownCommandSuggests: a typo or another CLI's verb names the tailworks
// command to run instead; a word near nothing gets no suggestion.
func TestUnknownCommandSuggests(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"deplo": "tw deploy", "Logs": "tw logs", "tail": "tw logs", "list": "tw ls", "secrets": "tw secret", "xyzzyqq": ""} {
		got := asCLIErr(unknownCommand(in)).Message
		if want == "" {
			if strings.Contains(got, "did you mean") {
				t.Errorf("tw %s: unexpected suggestion: %s", in, got)
			}
			continue
		}
		if !strings.Contains(got, "did you mean `"+want+"`") {
			t.Errorf("tw %s: want a suggestion of %q, got: %s", in, want, got)
		}
	}
	for v, c := range commandAliases {
		found := false
		for _, cmd := range commands {
			found = found || (cmd.name == c && !cmd.hidden)
		}
		if !found {
			t.Errorf("alias %s → %s: no such command", v, c)
		}
	}
}

// TestHelpFlagNames: a backquoted word in a flag's usage must not replace
// its type in `--help` ("-env wrangler --env" instead of "-env string").
func TestHelpFlagNames(t *testing.T) {
	bad := regexp.MustCompile(`(?m)^  -\S+ \S+ \S+`)
	for _, c := range commands {
		if c.hidden {
			continue
		}
		var so, se bytes.Buffer
		run([]string{c.name, "--help"}, &so, &se)
		if m := bad.FindString(so.String()); m != "" {
			t.Errorf("tw %s --help: flag line %q", c.name, m)
		}
	}
}
