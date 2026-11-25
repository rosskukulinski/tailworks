package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	repodocs "github.com/rosskukulinski/tailworks/docs"
)

// errorsCatalogFail is a literal code tw fails with: fail("<code>", …).
// The comma right after the literal skips dynamic codes such as
// fail("upgrade_"+run.State, …), which errorsCatalogDynamic lists.
var errorsCatalogFail = regexp.MustCompile(`\bfail\("([a-z0-9_]+)",`)

var errorsCatalogHeading = regexp.MustCompile(`(?m)^### ([a-z0-9_]+)$`)

// errorsCatalogDynamic are the codes tw builds at run time:
// fail("upgrade_"+run.State) in upgrade.go, for the fleet run states that
// stop a `tw upgrade` (mgmt/upgrades.go).
var errorsCatalogDynamic = []string{
	"upgrade_aborted",
	"upgrade_paused",
}

// Every code tw prints links to docs/errors.md#<code> (printErr), so each
// one needs a heading there (issue #529). Named DocRefCheck… so make
// test-docs runs it on a docs-only branch too.
func TestDocRefCheckErrorCodes(t *testing.T) {
	md, ok := repodocs.Read("errors.md")
	if !ok {
		t.Fatal("docs/errors.md is not embedded")
	}
	heads := map[string]bool{}
	for _, m := range errorsCatalogHeading.FindAllStringSubmatch(md, -1) {
		heads[m[1]] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range errorsCatalogFail.FindAllStringSubmatch(string(b), -1) {
			if _, seen := where[m[1]]; !seen {
				where[m[1]] = f
			}
		}
	}
	if len(where) < 50 {
		t.Fatalf("found only %d fail(\"<code>\", …) codes in cmd/tw: is the pattern stale?", len(where))
	}
	for _, c := range errorsCatalogDynamic {
		where[c] = "upgrade.go (dynamic)"
	}
	var missing []string
	for c, f := range where {
		if !heads[c] {
			missing = append(missing, c+" ("+f+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("docs/errors.md has no `### %s` entry (When/Fix)", m)
	}
}
