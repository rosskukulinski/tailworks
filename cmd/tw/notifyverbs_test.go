package main

import (
	"strings"
	"testing"
)

// TestNotifyTelemetryVerbs (issue #468): tw notify and tw telemetry find
// their verb with the shared scanner (show when none is given, flags with
// a value before it), and only set lists set's flags.
func TestNotifyTelemetryVerbs(t *testing.T) {
	for _, c := range []struct {
		args []string
		verb string
		rest string
	}{
		{nil, "show", ""},
		{[]string{"--tenant", "t", "--json"}, "show", "--tenant t --json"},
		{[]string{"--tenant", "t", "set", "--webhook", "-"}, "set", "--tenant t --webhook -"},
		{[]string{"--header", "authorization", "set"}, "set", "--header authorization"},
		{[]string{"--state-dir", "/d", "list"}, "ls", "--state-dir /d"},
	} {
		verb, rest := scanVerbOr(c.args, func(f *flags) { notifyFlags(f, anyVerb) }, "show")
		if verb != c.verb || strings.Join(rest, " ") != c.rest {
			t.Errorf("notify %v: %q %q", c.args, verb, rest)
		}
	}
	tr := buildCompletionTree()
	for path, c := range map[string]struct{ has, not []string }{
		"notify":        {[]string{"--tenant"}, []string{"--webhook", "--header"}},
		"notify set":    {[]string{"--tenant", "--webhook", "--header"}, nil},
		"notify rm":     {[]string{"--tenant"}, []string{"--webhook", "--header"}},
		"notify test":   {[]string{"--tenant"}, []string{"--webhook", "--header"}},
		"telemetry":     {[]string{"--tenant"}, []string{"--otlp", "--header", "--signals"}},
		"telemetry set": {[]string{"--tenant", "--otlp", "--header", "--signals"}, nil},
		"telemetry rm":  {[]string{"--tenant"}, []string{"--otlp", "--header", "--signals"}},
	} {
		n := tr.nodes[path]
		if n == nil {
			t.Errorf("completion has no path %q", path)
			continue
		}
		for _, fl := range c.has {
			if !n.flags[fl] {
				t.Errorf("tw %s lacks %s (has %v)", path, fl, tr.flagList(path))
			}
		}
		for _, fl := range c.not {
			if n.flags[fl] {
				t.Errorf("tw %s has set's flag %s", path, fl)
			}
		}
	}
	// A set flag before the verb reaches set (and stops at the missing
	// tailworks); on another verb it is refused.
	dir := t.TempDir() + "/never-created"
	for _, c := range []struct {
		run  func([]string) error
		args []string
		ok   bool
	}{
		{cmdTelemetry, []string{"--otlp", "http://otel:4318", "set"}, true},
		{cmdNotify, []string{"--webhook", "https://hooks.example.com/x", "set"}, true},
		{cmdTelemetry, []string{"rm", "--otlp", "http://otel:4318"}, false},
		{cmdNotify, []string{"--webhook", "https://hooks.example.com/x"}, false},
	} {
		err := c.run(append(c.args, "--state-dir", dir))
		undefined := strings.Contains(cliText(err), "not defined")
		if err == nil || undefined == c.ok {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}
