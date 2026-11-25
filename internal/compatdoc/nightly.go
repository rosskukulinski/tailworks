package compatdoc

import (
	"bufio"
	"bytes"
	"fmt"
	"go/build/constraint"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A ✅ or ⚠️ row should be backed by a test that every PR runs. When all of
// its tests are in suites that only run nightly (or by hand), its Tests cell
// must say "nightly", so the claim doesn't read as stronger than its
// evidence (issue #163).
//
// A test runs on every PR when ci.yml runs it: `make test` (a Go package
// outside e2e/, in a file with no build tag other than an OS or arch, or a
// JS test file outside e2e/), or one of the e2e suites ci.yml runs on its
// own (perPRSuites).

// perPRSuites are the e2e directories ci.yml runs on every PR besides
// `make test`. Keep in step with .github/workflows/ci.yml.
var perPRSuites = []string{
	"e2e/local",  // make e2e-local
	"e2e/vitest", // make test-vitest
}

// NightlyWord is what a Tests cell must contain when none of its tests run
// on every PR.
const NightlyWord = "nightly"

// checkNightly returns an error when r claims ✅/⚠️, none of its tests run
// on every PR, and its Tests cell doesn't say so.
func checkNightly(root string, r Row) error {
	if r.Status != Works && r.Status != Partial || len(r.Tests) == 0 {
		return nil
	}
	if strings.Contains(strings.ToLower(r.TestsCell), NightlyWord) {
		return nil
	}
	for _, ref := range r.Tests {
		if runsPerPR(root, ref) {
			return nil
		}
	}
	return fmt.Errorf("docs/COMPAT.md:%d: surface %q is %s but every test it names runs only in a nightly or by-hand suite; say %q in its Tests cell, or cite a test `make test` runs", r.Line, r.ID, r.Status, NightlyWord)
}

// runsPerPR reports whether ref names a test ci.yml runs on every PR.
func runsPerPR(root, ref string) bool {
	m := testRefRE.FindStringSubmatch(ref)
	if m == nil {
		return false
	}
	path := strings.TrimSuffix(m[1], "/")
	for _, s := range perPRSuites {
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	if path == "e2e" || strings.HasPrefix(path, "e2e/") {
		return false
	}
	p := filepath.Join(root, filepath.FromSlash(path))
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	if !fi.IsDir() {
		return true // a JS test file under `npm test`
	}
	name := strings.SplitN(m[2], "/", 2)[0]
	decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(t \*testing\.T\)`)
	files, _ := filepath.Glob(filepath.Join(p, "*_test.go"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || !decl.Match(b) {
			continue
		}
		return untaggedForTest(b)
	}
	return false
}

// untaggedForTest reports whether a Go file builds under plain `go test` on
// linux or darwin: no //go:build line, or one that only names an OS or arch.
func untaggedForTest(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return true
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		for _, plat := range [][]string{{"linux", "amd64"}, {"darwin", "arm64"}} {
			ok := expr.Eval(func(tag string) bool {
				return tag == plat[0] || tag == plat[1] || tag == "unix" || tag == "cgo" || strings.HasPrefix(tag, "go1.")
			})
			if ok {
				return true
			}
		}
		return false
	}
	return true
}
