package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// designDocsDir holds the RFC and the ADRs.
const designDocsDir = "../../docs/design"

// droppedForms are `tw` command lines the design once named that parse as
// a real command with an argument, so the command-name check alone would
// pass them: `tw policy lint` is `tw policy` on a directory named lint.
var droppedForms = []string{"policy lint", "policy test", "policy push"}

// twInCode finds `tw <sub> [<word>]` in code: not part of a longer word,
// path or name (tw-mgmt, tag:tw, @tw, .tw).
var twInCode = regexp.MustCompile(`(?:^|[^\w./@:-])tw ([a-z][a-z0-9-]*)(?: ([a-z][a-z0-9-]*))?`)

// inlineCode matches a Markdown code span.
var inlineCode = regexp.MustCompile("`[^`\n]+`")

// footnoted reports whether a line says the thing it names isn't there:
// the design may keep naming a command it never shipped, as long as the
// same line says so.
func footnoted(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "not built")
}

// TestDesignDocsNameRealCommands: every `tw <command>` that the RFC or an
// ADR names in code (a code span or a fenced block) exists in `commands`,
// unless the line says it was not built. The design docs drifted from the
// CLI before (`tw connect`, `tw policy lint`, `tw restore`, issue #101).
func TestDesignDocsNameRealCommands(t *testing.T) {
	t.Parallel()
	known := map[string]bool{"help": true}
	for _, c := range commands {
		if !c.hidden {
			known[c.name] = true
		}
	}
	var paths []string
	err := filepath.WalkDir(designDocsDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 40 {
		t.Fatalf("found only %d design docs under %s", len(paths), designDocsDir)
	}
	checked := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(designDocsDir, p)
		inFence := false
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			var code []string
			if inFence {
				code = []string{line}
			} else {
				code = inlineCode.FindAllString(line, -1)
			}
			for _, c := range code {
				for _, m := range twInCode.FindAllStringSubmatch(strings.Trim(c, "`"), -1) {
					checked++
					if footnoted(line) {
						continue
					}
					if !known[m[1]] {
						t.Errorf("%s:%d: `tw %s` is not a tw command; name what was built, or say on the same line that it was not built", rel, i+1, m[1])
						continue
					}
					for _, d := range droppedForms {
						if m[1]+" "+m[2] == d {
							t.Errorf("%s:%d: `tw %s` was never built; say so on the same line, or drop it", rel, i+1, d)
						}
					}
				}
			}
		}
	}
	// The check itself must catch what goes stale.
	if m := twInCode.FindStringSubmatch("A->>CLI: tw connect acme.com"); m == nil || known[m[1]] {
		t.Errorf("the check misses `tw connect`: %q", m)
	}
	if twInCode.MatchString("tw-mgmt init") || twInCode.MatchString("@tailworks/tw x") {
		t.Error("the check matches names that aren't tw commands")
	}
	if checked < 30 {
		t.Fatalf("checked only %d tw commands in the design docs", checked)
	}
	t.Logf("%d tw commands in %d design docs checked", checked, len(paths))
}
