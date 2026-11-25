package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpGroups: `tw help` lists every command exactly once, in a named
// group (none falls through to "Other"), with a short line; no line is
// long; `tw help --all` still has every full usage line.
func TestHelpGroups(t *testing.T) {
	titles, groups := groupedCommands()
	seen := map[string]int{}
	for i, title := range titles {
		if title == "Other" {
			var names []string
			for _, c := range groups[i] {
				names = append(names, c.name)
			}
			t.Errorf("commands in no help group (set group: and short: on their entry in commands, main.go): %s", strings.Join(names, ", "))
		}
		for _, c := range groups[i] {
			seen[c.name]++
		}
	}
	for _, c := range commands {
		if c.hidden {
			if seen[c.name] != 0 {
				t.Errorf("tw help lists the hidden command %s", c.name)
			}
			continue
		}
		if seen[c.name] != 1 {
			t.Errorf("tw help lists %s %d times", c.name, seen[c.name])
		}
		if c.short == "" {
			t.Errorf("no short: line for %s (its entry in commands, main.go)", c.name)
		}
		if l := helpLine(c); len([]rune(l)) > 60 {
			t.Errorf("tw help line for %s is %d characters (≤ 60): %q", c.name, len([]rune(l)), l)
		}
	}

	var so, se bytes.Buffer
	if code := run([]string{"help"}, &so, &se); code != 0 {
		t.Fatalf("tw help: exit %d", code)
	}
	for _, l := range strings.Split(so.String(), "\n") {
		if len([]rune(l)) > 130 {
			t.Errorf("tw help has a %d-character line: %q", len([]rune(l)), l)
		}
	}
	for _, g := range []string{"Set up:", "Develop:", "Coding agents:", "Deploy:", "Data:", "Observe:", "Operate:", "Scale out:", "Advanced:"} {
		if !strings.Contains(so.String(), g) {
			t.Errorf("tw help lacks the %q group", g)
		}
	}
	so.Reset()
	if code := run([]string{"help", "--all"}, &so, &se); code != 0 {
		t.Fatalf("tw help --all: exit %d", code)
	}
	for _, c := range commands {
		if !c.hidden && !hasUsage(so.String(), c) {
			t.Errorf("tw help --all lacks %s's usage", c.name)
		}
	}
}
