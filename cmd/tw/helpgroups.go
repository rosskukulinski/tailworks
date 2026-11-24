package main

import (
	"fmt"
	"io"
	"strings"
)

// `tw help` groups the commands with one short line each (issue #46);
// the full usage is `tw <command> --help`, every command's at once
// `tw help --all`.
//
// Each command names its group and its short line in its own entry in
// `commands` (main.go), so adding a command never edits a shared list here.
// Within a group, commands appear in `commands` order. A visible command
// with no group is listed under "Other" (TestHelpGroups fails on it).

// helpGroup is a `tw help` group title.
type helpGroup string

const (
	groupSetUp   helpGroup = "Set up"
	groupDevelop helpGroup = "Develop"
	groupDeploy  helpGroup = "Deploy"
	groupData    helpGroup = "Data"
	groupObserve helpGroup = "Observe"
	groupOperate helpGroup = "Operate"
)

// helpGroupOrder is the order of the groups in `tw help`.
var helpGroupOrder = []helpGroup{
	groupSetUp,
	groupDevelop,
	groupDeploy,
	groupData,
	groupObserve,
	groupOperate,
}

// helpLine is c's line in `tw help`: c.short, else the help text up to
// its first ";" or " (".
func helpLine(c command) string {
	if c.short != "" {
		return c.short
	}
	s := c.help
	for _, sep := range []string{";", " ("} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// groupedCommands is `tw help`'s layout: each group's visible commands,
// then "Other" for the visible commands in no known group.
func groupedCommands() (titles []string, groups [][]command) {
	known := map[helpGroup]bool{}
	for _, g := range helpGroupOrder {
		known[g] = true
		var cs []command
		for _, c := range commands {
			if !c.hidden && c.group == g {
				cs = append(cs, c)
			}
		}
		if len(cs) > 0 {
			titles, groups = append(titles, string(g)), append(groups, cs)
		}
	}
	var other []command
	for _, c := range commands {
		if !c.hidden && !known[c.group] {
			other = append(other, c)
		}
	}
	if len(other) > 0 {
		titles, groups = append(titles, "Other"), append(groups, other)
	}
	return titles, groups
}

// usage is `tw help` (and `tw`, `tw --help`).
func usage(w io.Writer) { usageGrouped(w) }

// usageGrouped is `tw help`.
func usageGrouped(w io.Writer) {
	fmt.Fprintln(w, "tw: Workers and Durable Objects with their own tailnet identities")
	fmt.Fprintln(w, "\nusage: tw <command> [flags]   (every command takes --state-dir, default ~/.tailworks, and --json)")
	titles, groups := groupedCommands()
	for i, t := range titles {
		fmt.Fprintf(w, "\n%s:\n", t)
		for _, c := range groups[i] {
			fmt.Fprintf(w, "  %-11s %s\n", c.name, helpLine(c))
		}
	}
	fmt.Fprintln(w, "\ntemplates (tw init --template): hello | agent | mcp | ai-agent | chat-agent | pi-agent | lock |")
	fmt.Fprintln(w, "  refund-approval | node-workflow (needs Node.js)")
	fmt.Fprintln(w, "\ntw <command> --help     a command's usage and flags")
	fmt.Fprintln(w, "tw help --all           every command's usage")
	fmt.Fprintln(w, "tw completion --help    tab completion for bash, zsh and fish")
}
