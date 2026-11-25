package main

import (
	"strings"
	"testing"
)

// On a terminal, or with --json, tw mcp prints the --config help instead
// of blocking on stdin (issue #466); with stdin a pipe it is the bridge.
func TestMCPShowConfig(t *testing.T) {
	asked := false
	term := func(v bool) func() bool { return func() bool { asked = true; return v } }
	for _, c := range []struct {
		name                   string
		config, json, terminal bool
		show, note             bool
	}{
		{name: "pipe: the bridge"},
		{name: "terminal", terminal: true, show: true, note: true},
		{name: "--json", json: true, show: true},
		{name: "--json on a terminal", json: true, terminal: true, show: true},
		{name: "--config", config: true, show: true},
		{name: "--config on a terminal", config: true, terminal: true, show: true},
	} {
		asked = false
		show, note := mcpShowConfig(c.config, c.json, term(c.terminal))
		if show != c.show || (note != "") != c.note {
			t.Errorf("%s: show %v note %q, want show %v note %v", c.name, show, note, c.show, c.note)
		}
		if note != "" && !strings.Contains(note, "| tw mcp") {
			t.Errorf("%s: the note should say how to pipe into the bridge: %q", c.name, note)
		}
		if (c.config || c.json) && asked {
			t.Errorf("%s: asked whether stdin is a terminal when it didn't matter", c.name)
		}
	}
}
