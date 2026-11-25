package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

// CLISchema is cli.schema.json: the shape of every tailworks command's --json
// output (docs/cli-json.md). Its $defs are named after the command
// ("deploy", "tenant ls"); "<command>.event" is one NDJSON event and
// "error" is the error object every command prints on stderr.
//
//go:embed cli.schema.json
var CLISchema []byte

var cliRoot = func() *node {
	var n node
	if err := json.Unmarshal(CLISchema, &n); err != nil {
		panic("schema: cli.schema.json: " + err.Error())
	}
	return &n
}()

// CLIDefs lists the schema's $defs.
func CLIDefs() []string {
	out := make([]string, 0, len(cliRoot.Defs))
	for k := range cliRoot.Defs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CheckCLI checks decoded JSON v against $defs/<def>.
func CheckCLI(def string, v any) ([]Problem, error) {
	n, ok := cliRoot.Defs[def]
	if !ok {
		return nil, fmt.Errorf("schema/cli.schema.json has no $defs/%q", def)
	}
	var out []Problem
	check(cliRoot, n, v, "", &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
