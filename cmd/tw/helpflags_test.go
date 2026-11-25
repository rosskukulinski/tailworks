package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

// helpFlags is the flag set (with its help layout) `tw <path> --help`
// builds, or nil.
func helpFlags(c command, sub []string) (f *flags) {
	defer func() {
		if recover() != nil {
			f = nil
		}
	}()
	saved := jsonOut
	defer func() { jsonOut = saved }()
	var hr *helpRequest
	if err := c.run(append(append([]string{}, sub...), "--help")); errors.As(err, &hr) {
		return hr.f
	}
	return nil
}

// TestFlagGroupsResolve: every name a command groups or hides is one of
// its flags, in one place only, and a flag whose help says it is for tests
// is hidden.
func TestFlagGroupsResolve(t *testing.T) {
	tree := buildCompletionTree()
	cmds := map[string]command{}
	for _, c := range commands {
		cmds[c.name] = c
	}
	for _, p := range tree.paths() {
		w := strings.Fields(p)
		if len(w) == 0 {
			continue
		}
		f := helpFlags(cmds[w[0]], w[1:])
		if f == nil {
			continue
		}
		seen := map[string]string{}
		hidden := map[string]bool{}
		check := func(where string, names []string) {
			for _, n := range names {
				if f.Lookup(n) == nil {
					t.Errorf("tw %s: %s names --%s, which is not a flag", p, where, n)
				}
				if prev, ok := seen[n]; ok {
					t.Errorf("tw %s: --%s is in %s and %s", p, n, prev, where)
				}
				seen[n] = where
			}
		}
		for _, g := range f.help.groups {
			check("group "+g.title, g.names)
		}
		for _, h := range f.help.hidden {
			check("hidden ("+h.title+")", h.names)
			for _, n := range h.names {
				hidden[n] = true
			}
		}
		f.VisitAll(func(fl *flag.Flag) {
			for _, s := range []string{"(tests)", "(testing)", "DEVELOPMENT ONLY"} {
				if strings.Contains(fl.Usage, s) && !hidden[fl.Name] {
					t.Errorf("tw %s: --%s says %q but is listed in --help; hide it (f.hidden)", p, fl.Name, s)
				}
			}
		})
	}
}

// TestFlagHelpLayout: tw up --help lists the everyday flags first, then
// its groups; hidden flags only by name; the global flags once, on the
// last line.
func TestFlagHelpLayout(t *testing.T) {
	var so, se bytes.Buffer
	if code := run([]string{"up", "--help"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	out := so.String()
	i := strings.Index(out, "\nflags:\n")
	if i < 0 {
		t.Fatalf("no flags block:\n%s", out)
	}
	at := i
	for _, want := range []string{"  -oauth-client-file string", "\n" + upGroupControl + ":\n", "  -control-url string", "\n" + upGroupStore + ":\n", "  -db-file string", "\n" + upGroupRetention + ":\n", "\n" + upGroupIsolation + ":\n", "  -worker-internet string", "\n" + upGroupDev + ":\n", "also accepted, for the test harness: --allow-sibling-egress --local-dev --tsapi-url\n", "global: --state-dir"} {
		j := strings.Index(out[at:], want)
		if j < 0 {
			t.Fatalf("tw up --help: %q missing or out of order:\n%s", want, out)
		}
		at += j
	}
	for _, bad := range []string{"  -tsapi-url", "  -local-dev", "  -state-dir", "  -json"} {
		if strings.Contains(out, bad) {
			t.Errorf("tw up --help lists %q:\n%s", bad, out)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "· --json") {
		t.Errorf("tw up --help: want the global line last:\n%s", out)
	}
	// A hidden flag still parses (and so still completes and is saved).
	if fs := probeFlags(cmds("up"), nil); fs == nil || fs.Lookup("local-dev") == nil || fs.Lookup("tsapi-url") == nil {
		t.Error("tw up: hidden flags left the flag set")
	}

	// --tenant from tenantFlag moves to the global line.
	so.Reset()
	run([]string{"ls", "--help"}, &so, &se)
	if out := so.String(); strings.Contains(out, "  -tenant") || !strings.Contains(out, "· --tenant <tailnet|id>") || !strings.Contains(out, "\nAdvanced:\n  -limit int") {
		t.Errorf("tw ls --help: want --tenant on the global line and --limit under Advanced:\n%s", out)
	}
}

func cmds(name string) command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	panic(name)
}

// TestYesFlagWording: every --yes says the same thing.
func TestYesFlagWording(t *testing.T) {
	tree := buildCompletionTree()
	for _, p := range tree.paths() {
		w := strings.Fields(p)
		if len(w) == 0 {
			continue
		}
		fs := probeFlags(cmds(w[0]), w[1:])
		if fs == nil {
			continue
		}
		if fl := fs.Lookup("yes"); fl != nil && !strings.HasPrefix(fl.Usage, "skip the confirmation before ") {
			t.Errorf("tw %s --yes: %q; register it with yesFlagBefore", p, fl.Usage)
		}
	}
}
