// Command changes reads release-note fragments (changes/*.md) for the
// release scripts:
//
//	changes validate FILE...                 check every fragment's front matter
//	changes kinds FILE                       print the fragment's upgrade kinds, one per line
//	changes notes [-title T] FILE...         print the assembled release notes (markdown)
//	changes changelog                        print CHANGELOG.md, from the embedded release catalog
//	changes entry -version V -date D -workerd W [-schema S] [-contract a,b] [-protocols JSON] FILE...
//	                                         print the release catalog entry (JSON)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/twd/internal/catalog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "changes:", err)
		os.Exit(1)
	}
}

func readAll(files []string) ([]catalog.Note, error) {
	var notes []catalog.Note
	var errs []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		n, err := catalog.ParseFragment(filepath.Base(f), b)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		notes = append(notes, n)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid fragments:\n  %s", strings.Join(errs, "\n  "))
	}
	return notes, nil
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: changes validate|kinds|notes|entry …")
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "validate":
		_, err := readAll(rest)
		return err
	case "kinds":
		notes, err := readAll(rest)
		if err != nil {
			return err
		}
		for _, k := range catalog.Union(notes) {
			fmt.Println(k)
		}
		return nil
	case "notes":
		f := flag.NewFlagSet("notes", flag.ContinueOnError)
		title := f.String("title", "", "heading")
		if err := f.Parse(rest); err != nil {
			return err
		}
		notes, err := readAll(f.Args())
		if err != nil {
			return err
		}
		fmt.Print(catalog.Markdown(*title, notes))
		return nil
	case "changelog":
		fmt.Print(catalog.Changelog(catalog.Embedded()))
		return nil
	case "entry":
		f := flag.NewFlagSet("entry", flag.ContinueOnError)
		v := f.String("version", "", "release version (0.2.0)")
		date := f.String("date", "", "YYYY-MM-DD")
		wd := f.String("workerd", "", "workerd pin")
		schema := f.String("schema", "", "newest mgmt migration ID")
		contract := f.String("contract", "", "comma-separated contract migration IDs")
		protocols := f.String("protocols", "", "protocol map (JSON)")
		if err := f.Parse(rest); err != nil {
			return err
		}
		notes, err := readAll(f.Args())
		if err != nil {
			return err
		}
		e := catalog.Entry{Version: strings.TrimPrefix(*v, "v"), Date: *date, Workerd: *wd, Schema: *schema,
			Upgrade: catalog.Union(notes), Notes: notes}
		if e.Notes == nil {
			e.Notes = []catalog.Note{}
		}
		for _, c := range strings.Split(*contract, ",") {
			if c = strings.TrimSpace(c); c != "" {
				e.ContractMigrations = append(e.ContractMigrations, c)
			}
		}
		if *protocols != "" {
			if err := json.Unmarshal([]byte(*protocols), &e.Protocols); err != nil {
				return fmt.Errorf("-protocols: %w", err)
			}
		}
		b, _ := json.MarshalIndent(e, "", "  ")
		if _, err := catalog.Parse(b); err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
