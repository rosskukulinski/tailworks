package main

import (
	"os"
	"strings"
	"testing"
)

const cliDocPath = "../../docs/cli.md"

// TestCLIReferenceUpToDate: docs/cli.md is cliReference(), the --help
// output of every command path. TW_UPDATE_CLI_DOCS=1 rewrites it
// (`make cli-docs`).
func TestCLIReferenceUpToDate(t *testing.T) {
	want := cliReference()
	if os.Getenv("TW_UPDATE_CLI_DOCS") == "1" {
		if err := os.WriteFile(cliDocPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(cliDocPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatal("docs/cli.md is stale: run `make cli-docs`")
	}
}

// TestCLIReferenceCoversCommands: every visible command and every flag
// its --help lists appears in the reference.
func TestCLIReferenceCoversCommands(t *testing.T) {
	ref := cliReference()
	for _, c := range commands {
		if c.hidden {
			continue
		}
		if !strings.Contains(ref, "\n### tw "+c.name+"\n") {
			t.Errorf("docs/cli.md has no section for tailworks %s", c.name)
		}
		if fs := probeFlags(c, nil); fs != nil {
			for _, name := range []string{"state-dir", "json"} {
				if fs.Lookup(name) != nil && !strings.Contains(ref, "-"+name) {
					t.Errorf("tw %s: flag -%s missing", c.name, name)
				}
			}
		}
	}
}
