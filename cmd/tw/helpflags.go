package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// `tw <cmd> --help` lists a command's flags in groups (issue #463):
//
//	flags:            the everyday flags (always first: clidoc.go finds a
//	                  flag block by its "\nflags:\n")
//	<title>:          each f.group, in the order the groups were declared
//	also accepted, …: one line naming the f.hidden flags
//	global: …         --state-dir, --json and the tenant flag, once
//
// Groups and hidden flags change only what --help (and so docs/cli.md)
// prints. Every flag stays in the FlagSet, so parsing, completion, the
// saved tw up flags and the docs checks see all of them. A command with no
// groups prints its flags as one list, as flag.PrintDefaults does.

// advancedFlags is the group title for flags most runs don't need.
const advancedFlags = "Advanced"

// flagHelp is how --help lays out a flag set (flags.help).
type flagHelp struct {
	groups []flagGroup
	hidden []flagGroup // title: why they're accepted ("for the test harness")
}

type flagGroup struct {
	title string
	names []string
}

// group lists names under title in --help, after the everyday flags.
// Declaring a title again adds to that group, so a group's position can be
// declared before the flags it gets (tw up's store flags join theirs in
// up_store.go). Names that aren't flags are ignored here;
// TestFlagGroupsResolve fails on them.
func (f *flags) group(title string, names ...string) {
	for i := range f.help.groups {
		if f.help.groups[i].title == title {
			f.help.groups[i].names = append(f.help.groups[i].names, names...)
			return
		}
	}
	f.help.groups = append(f.help.groups, flagGroup{title: title, names: names})
}

// hidden leaves names out of --help's flag lists: one line names them,
// "also accepted, <why>: --a --b". They parse and complete as before.
func (f *flags) hidden(why string, names ...string) {
	f.help.hidden = append(f.help.hidden, flagGroup{title: why, names: names})
}

// tenantUsage is tenantFlag's help text; a --tenant with it is printed
// on the global line.
const tenantUsage = "tenant: its tailnet, tenant ID or tailnet label (default: the `tw up` tenant)"

// globalFlagNames are the flags newFlags registers on every command.
var globalFlagNames = []string{
	"state-dir",
	"json",
}

// yesFlagBefore registers --yes for a command that asks before it acts:
// one wording for every --yes ("skip the confirmation before <verb>").
func yesFlagBefore(f *flags, verb string) *bool {
	return f.Bool("yes", false, "skip the confirmation before "+verb+"; needed without a terminal and with --json")
}

// writeFlagHelp prints f's flags: the everyday ones, each group, the
// hidden ones' names and the global line.
func writeFlagHelp(w io.Writer, f *flags) {
	placed := map[string]bool{}
	var global []string
	for _, n := range globalFlagNames {
		if f.Lookup(n) != nil {
			placed[n] = true
			global = append(global, n)
		}
	}
	if fl := f.Lookup("tenant"); fl != nil && fl.Usage == tenantUsage {
		placed["tenant"] = true
		global = append(global, "tenant")
	}
	var hiddenLines []string
	for _, h := range f.help.hidden {
		var names []string
		for _, n := range h.names {
			if f.Lookup(n) != nil && !placed[n] {
				placed[n] = true
				names = append(names, "--"+n)
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			hiddenLines = append(hiddenLines, "also accepted, "+h.title+": "+strings.Join(names, " "))
		}
	}
	groups := make([][]*flag.Flag, len(f.help.groups))
	for i, g := range f.help.groups {
		for _, n := range g.names {
			if fl := f.Lookup(n); fl != nil && !placed[n] {
				placed[n] = true
				groups[i] = append(groups[i], fl)
			}
		}
	}
	plainUsages(f.FlagSet) // didyoumean.go
	var common []*flag.Flag
	f.VisitAll(func(fl *flag.Flag) {
		if !placed[fl.Name] {
			common = append(common, fl)
		}
	})

	fmt.Fprintln(w, "\nflags:")
	if len(common) == 0 {
		fmt.Fprintln(w, "  (none besides the global ones)")
	}
	printFlagDefaults(w, common)
	for i, g := range f.help.groups {
		if len(groups[i]) > 0 {
			fmt.Fprintf(w, "\n%s:\n", g.title)
			printFlagDefaults(w, groups[i])
		}
	}
	if len(hiddenLines) > 0 || len(global) > 0 {
		fmt.Fprintln(w)
	}
	for _, l := range hiddenLines {
		fmt.Fprintln(w, l)
	}
	if len(global) > 0 {
		fmt.Fprintln(w, "global: "+globalLine(global))
	}
}

// globalLine describes the global flags present, in names' order.
func globalLine(names []string) string {
	desc := map[string]string{
		"state-dir": "--state-dir <dir> (default $TW_STATE_DIR or ~/.tailworks)",
		"json":      "--json",
		"tenant":    "--tenant <tailnet|id> (default: the tw up tenant)",
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, desc[n])
	}
	return strings.Join(parts, " · ")
}

// printFlagDefaults prints fls as flag.PrintDefaults would (sorted, the
// stdlib's line format), through a scratch FlagSet sharing their values.
func printFlagDefaults(w io.Writer, fls []*flag.Flag) {
	if len(fls) == 0 {
		return
	}
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	for _, fl := range fls {
		fs.Var(fl.Value, fl.Name, fl.Usage)
		fs.Lookup(fl.Name).DefValue = fl.DefValue
	}
	fs.SetOutput(w)
	fs.PrintDefaults()
}
