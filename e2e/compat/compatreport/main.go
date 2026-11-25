// Command compatreport keeps the upstream-examples tables of docs/COMPAT.md
// (issue #325) in step with the suite: see e2e/compat/report.go.
//
//	compatreport -check                       # make compat-upstream-check (in make lint)
//	compatreport [-dev f.json] [-deployed f.json]   # make compat-upstream
//	compatreport -print                       # the tables, to stdout (the nightly's job summary)
//
// Without -dev / -deployed, the update takes the newest
// e2e/real/results/compat-dev-*.json and compat-2*.json when there are any.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rosskukulinski/tailworks/e2e/compat"
)

func main() {
	check := flag.Bool("check", false, "verify docs/COMPAT.md and "+compat.ResultsFile+" against the project list; write nothing")
	print := flag.Bool("print", false, "print the tables for the given run files; write nothing")
	dev := flag.String("dev", "", "results file of `make e2e-compat-dev`")
	deployed := flag.String("deployed", "", "results file of `make e2e-compat`")
	flag.Parse()
	if err := run(*check, *print, *dev, *deployed); err != nil {
		fmt.Fprintln(os.Stderr, "compatreport:", err)
		os.Exit(1)
	}
}

// newest is the newest file matching pattern under root ("" when none).
func newest(root, pattern string) string {
	m, _ := filepath.Glob(filepath.Join(root, "e2e", "real", "results", pattern))
	sort.Strings(m) // the names carry the time
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

func run(check, print bool, dev, deployed string) error {
	const root = "." // run from the repo root (make)
	doc := filepath.Join(root, "docs", "COMPAT.md")
	md, err := os.ReadFile(doc)
	if err != nil {
		return err
	}
	if check {
		pub, err := compat.LoadPublished(root)
		if err != nil {
			return err
		}
		if err := pub.Verify(); err != nil {
			return err
		}
		want, err := compat.UpdateDoc(md, pub)
		if err != nil {
			return err
		}
		if !bytes.Equal(want, md) {
			return fmt.Errorf("docs/COMPAT.md's upstream-examples tables differ from what %s and e2e/compat's project list render: run `make compat-upstream`", compat.ResultsFile)
		}
		return nil
	}
	pub := &compat.Published{}
	if !print {
		if old, err := compat.LoadPublished(root); err == nil {
			pub = old
		}
		if dev == "" {
			dev = newest(root, "compat-dev-*.json")
		}
		if deployed == "" {
			deployed = newest(root, "compat-2*.json")
		}
	}
	if dev != "" {
		rec, err := compat.LoadRunFile(dev, "dev", !print)
		if err != nil {
			return err
		}
		pub.Dev = compat.Merge(pub.Dev, rec)
	}
	if deployed != "" {
		rec, err := compat.LoadRunFile(deployed, "deployed", !print)
		if err != nil {
			return err
		}
		pub.Deployed = compat.Merge(pub.Deployed, rec)
	}
	if print {
		for _, s := range compat.Sections {
			fmt.Printf("## %s (%s)\n\n%s", s.Title, s.Repo, compat.RenderSection(s.ID, pub))
		}
		return nil
	}
	if err := pub.Verify(); err != nil {
		return err
	}
	out, err := compat.UpdateDoc(md, pub)
	if err != nil {
		return err
	}
	if err := pub.Save(root); err != nil {
		return err
	}
	return os.WriteFile(doc, out, 0o644)
}
