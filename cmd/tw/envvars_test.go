package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// envVarsRead matches a TW_* variable read in Go source.
var envVarsRead = regexp.MustCompile(`(?:Getenv|LookupEnv)\("(TW_[A-Z0-9_]+)"\)`)

// envVarsDirs are the packages, relative to cmd/tw, that the tw binary
// links and that read the environment at run time. The test harnesses
// (hostd/hostdtest, netd/nettest, e2e/) and the daemons' own main packages
// are left out: their variables are not tw's.
var envVarsDirs = []string{
	".",
	"../../internal/hostdrun",
	"../../hostd",
}

var envVarsSkip = []string{
	"hostdtest",
}

// docs/cli.md's Environment section lists exactly the TW_* variables the
// tw binary reads (issue #528). Named CLIReference… so make test-docs runs it.
func TestCLIReferenceEnvVars(t *testing.T) {
	read := map[string]string{}
	for _, dir := range envVarsDirs {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				for _, s := range envVarsSkip {
					if d.Name() == s {
						return filepath.SkipDir
					}
				}
				if dir == "." && p != "." { // cmd/tw: its own files only
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range envVarsRead.FindAllStringSubmatch(string(b), -1) {
				if _, ok := read[m[1]]; !ok {
					read[m[1]] = p
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	listed := map[string]bool{}
	for _, v := range append(append([]envVar{}, envVars...), envDebugVars...) {
		if v.ScriptsOnly || !strings.HasPrefix(v.Name, "TW_") {
			continue
		}
		listed[v.Name] = true
	}
	var errs []string
	for n, p := range read {
		if !listed[n] {
			errs = append(errs, n+" is read ("+p+") but not in envVars or envDebugVars (envvars.go)")
		}
	}
	for n := range listed {
		if _, ok := read[n]; !ok {
			errs = append(errs, n+" is in envvars.go but nothing in the tw binary reads it")
		}
	}
	sort.Strings(errs)
	for _, e := range errs {
		t.Error(e)
	}
	if !strings.Contains(cliReference(), "\n## Environment\n") {
		t.Error("docs/cli.md has no Environment section")
	}
}
