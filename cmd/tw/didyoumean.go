package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/schema"
)

// commandAliases maps the verbs of other CLIs (wrangler, vercel, fly) to
// tailworks' command, for the "did you mean" of an unknown command.
var commandAliases = map[string]string{
	"delete":  "undeploy",
	"destroy": "undeploy",
	"list":    "ls",
	"login":   "link",
	"ps":      "ls",
	"secrets": "secret",
	"start":   "dev",
	"tail":    "logs",
}

// unknownCommand is the error for `tw <name>` when no command has that
// name: it suggests the nearest command (a typo) or tailworks' name for another
// CLI's verb (wrangler tail → tw logs).
func unknownCommand(name string) error {
	var names []string
	for _, c := range commands {
		if !c.hidden {
			names = append(names, c.name)
		}
	}
	sugg := commandAliases[strings.ToLower(name)]
	if sugg == "" {
		sugg = schema.Closest(names, name)
	}
	if sugg != "" {
		return fail("bad_request", fmt.Sprintf("unknown command %q (did you mean `tw %s`?)", name, sugg), "run `tw help` for the list").
			withFix("tw " + sugg + " --help")
	}
	return fail("bad_request", fmt.Sprintf("unknown command %q", name), "run `tw help` for the list")
}

// plainUsages drops backquotes from flag usages before PrintDefaults: the
// flag package takes the first `quoted` word as the argument's name, so
// "as `wrangler --env` does" printed "-env wrangler --env".
func plainUsages(fs *flag.FlagSet) {
	fs.VisitAll(func(fl *flag.Flag) {
		fl.Usage = strings.ReplaceAll(fl.Usage, "`", "'")
	})
}
