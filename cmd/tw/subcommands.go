package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// A dispatching command's usage line is its subcommands' usage, one
// alternative per subcommand, joined by " | " ("tenant add <tailnet> … |
// tenant ls | …"), optionally ending in flags every subcommand takes after
// three spaces ("kv key list [--prefix P]   [--workload <workload>]").
// `tw <cmd> --help`, `tw help --all` and the "needs a subcommand" error
// list the subcommands from that line (issue #438), as completion does
// (completion.go), so there is no second list to keep in step.

// usageCommon matches the flags every subcommand takes, after three
// spaces at the end of the last alternative.
var usageCommon = regexp.MustCompile(`\s{3,}(\[.*)$`)

// usageAlts splits c's usage line into its alternatives, each starting
// with the command name. A segment that doesn't ("d1 execute <db>
// --command <sql> | --file <file>") stays part of the one before it.
// common is the trailing flags every subcommand takes, if any.
func usageAlts(c command) (alts []string, common string) {
	for _, seg := range compAltSep.Split(c.usage, -1) {
		w := strings.Fields(seg)
		if len(alts) > 0 && (len(w) == 0 || w[0] != c.name) {
			alts[len(alts)-1] += " | " + seg
			continue
		}
		alts = append(alts, seg)
	}
	if n := len(alts); n > 0 {
		if m := usageCommon.FindStringSubmatchIndex(alts[n-1]); m != nil {
			common = alts[n-1][m[2]:]
			alts[n-1] = alts[n-1][:m[0]]
		}
	}
	return alts, common
}

// subcommandWords are c's subcommands as its usage line names them, the
// second word grouped under the first: "create", "migrations
// create|list|apply", "on|off". None for a command without subcommands.
func subcommandWords(c command) []string {
	var out []string
	at := map[string]int{}
	alts, _ := usageAlts(c)
	for _, alt := range alts {
		w := strings.Fields(alt)
		var path []string
		for _, tok := range w[1:] {
			if !compLiteral.MatchString(tok) && !allLiteral(strings.Split(tok, "|")) {
				break
			}
			path = append(path, tok)
			if len(path) == 2 {
				break
			}
		}
		if len(path) == 0 {
			continue
		}
		i, ok := at[path[0]]
		if !ok {
			at[path[0]] = len(out)
			out = append(out, strings.Join(path, " "))
			continue
		}
		if len(path) == 2 {
			out[i] += "|" + path[1]
		}
	}
	return out
}

// needsSubcommand is the usage error of a dispatching command run without
// a subcommand.
func needsSubcommand(name string) *cliErr {
	for _, c := range commands {
		if c.name == name {
			return fail("bad_request", "tw "+name+" needs a subcommand: "+strings.Join(subcommandWords(c), ", "),
				"tw "+name+" --help: each subcommand's usage")
		}
	}
	return fail("bad_request", "tw "+name+" needs a subcommand")
}

// writeSubcommands prints c's usage alternatives one per line, each
// indented by indent and starting "tw ", then the flags they share.
// It reports false (and prints nothing) for a command without
// subcommands.
func writeSubcommands(w io.Writer, c command, indent string) bool {
	if len(subcommandWords(c)) == 0 {
		return false
	}
	alts, common := usageAlts(c)
	for _, alt := range alts {
		fmt.Fprintf(w, "%stw %s\n", indent, alt)
	}
	if common != "" {
		fmt.Fprintf(w, "%scommon flags: %s\n", indent, common)
	}
	return true
}

// subcommandHelp is the top of `tw <cmd> --help` for a command with
// subcommands: each one's usage, and when the subcommands own their flag
// sets (f == nil), how to see them. It reports false (and prints
// nothing) for a command without subcommands, whose usage line is
// printed as it is.
func subcommandHelp(w io.Writer, c command, f *flags) bool {
	var b strings.Builder
	if !writeSubcommands(&b, c, "  ") {
		return false
	}
	fmt.Fprintf(w, "subcommands:\n%s", b.String())
	if f == nil {
		fmt.Fprintf(w, "\ntw %s <subcommand> --help   its flags\n", c.name)
	}
	return true
}
