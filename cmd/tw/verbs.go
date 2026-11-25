package main

import (
	"flag"
	"strings"
)

// Subcommand verbs, shared by every command that has subcommands (issues
// #461, #462).

// verbAliases are the spellings of tw's two common verbs that the
// commands also accept (wrangler's list and delete, and the usual
// others). The usage lines and docs spell them ls and rm.
var verbAliases = map[string]string{
	"del":    "rm",
	"delete": "rm",
	"list":   "ls",
	"remove": "rm",
}

// normalizeVerb maps list to ls and remove, delete and del to rm; any other
// word is returned as it is. A command calls it on its verb before
// dispatching, so every spelling reaches the same code (and the same
// confirmDestroy guard).
func normalizeVerb(w string) string {
	if v, ok := verbAliases[w]; ok {
		return v
	}
	return w
}

// anyVerb asks a command's flag registration (kvFlags, d1Flags, …) for
// the flags of every verb: scanVerb needs them all to know which flags
// take a value.
const anyVerb = "*"

// onVerb reports whether the verb being registered is one of verbs (or
// anyVerb). A flag registration reads:
//
//	if onVerb(verb, "key put") { o.path = f.String("path", …) }
func onVerb(verb string, verbs ...string) bool {
	if verb == anyVerb {
		return true
	}
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// scanVerb finds a command's subcommand: its first positional words, with
// flags before, between or after them (tw kv --workload app key get k).
// register declares every flag the command's verbs take (anyVerb), so a
// flag's value (--workload app) is never taken for a word. words says how
// many words name the verb, given the first (d1: two after "migrations").
//
// It returns the words joined by a space, the last one through
// normalizeVerb (kv key list → kv key ls), and the other args in their
// order, for the verb's own flag set to parse. complete is false when
// args hold fewer words than that (no verb, or "kv key" alone).
func scanVerb(args []string, register func(*flags), words func(first string) int) (verb string, rest []string, complete bool) {
	known := newFlags("scan")
	register(known)
	var ws []string
	want := 1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if len(ws) < want && (a == "-" || !strings.HasPrefix(a, "-")) {
			ws = append(ws, a)
			if len(ws) == 1 {
				want = words(a)
			}
			continue
		}
		rest = append(rest, a)
		if takesValue(known.FlagSet, a) && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	if len(ws) > 0 {
		ws[len(ws)-1] = normalizeVerb(ws[len(ws)-1])
	}
	return strings.Join(ws, " "), rest, len(ws) == want
}

// scanVerbOr is scanVerb for a command whose subcommand is one word and
// may be left out: def when none is given (tw notify and tw telemetry
// show).
func scanVerbOr(args []string, register func(*flags), def string) (verb string, rest []string) {
	verb, rest, given := scanVerb(args, register, func(string) int { return 1 })
	if !given {
		verb = def
	}
	return verb, rest
}

// takesValue reports whether a is a flag of fs, spelled without =value,
// that reads the next argument as its value (not a bool flag).
func takesValue(fs *flag.FlagSet, a string) bool {
	if !strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
		return false
	}
	fl := fs.Lookup(strings.TrimLeft(a, "-"))
	if fl == nil {
		return false
	}
	bf, ok := fl.Value.(interface{ IsBoolFlag() bool })
	return !ok || !bf.IsBoolFlag()
}
