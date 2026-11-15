package main

// twd docs (issue #25): twd's docs, embedded in the binary, for people and
// coding agents with no network or no repo checkout.
//
//	twd docs search <words…> [--limit 10]
//	twd docs show <doc>[#<anchor>]      e.g. errors.md#acl_denied, ../README.md
//	twd docs ls

import (
	"fmt"
	"strings"

	twddocs "github.com/rosskukulinski/twd/docs"
)

func cmdDocs(args []string) error {
	sub, args, ok := splitSubcommand(args) // agent.go
	if !ok {
		return fail("bad_request", "twd docs needs a subcommand: search, show or ls", "usage: twd "+usageOf("docs"))
	}
	f := newFlags("docs")
	limit := f.Int("limit", 10, "search: at most this many sections")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	switch sub {
	case "ls":
		ds := twddocs.List()
		emit(f, map[string]any{"docs": ds}, func() {
			for _, d := range ds {
				fmt.Printf("  %-44s %s\n", d.Path, d.Title)
			}
		})
		return nil
	case "show":
		if len(pos) != 1 {
			return fail("bad_request", "twd docs show needs one doc, e.g. errors.md#acl_denied", "usage: twd "+usageOf("docs"))
		}
		md, ok := twddocs.Show(pos[0])
		if !ok {
			return fail("not_found", "no doc or section "+pos[0]).withFix("twd docs ls", "twd docs search <words>")
		}
		emit(f, map[string]any{"ref": pos[0], "markdown": md}, func() { fmt.Print(md) })
		return nil
	case "search":
		if len(pos) == 0 {
			return fail("bad_request", "twd docs search needs words to look for", "usage: twd "+usageOf("docs"))
		}
		q := strings.Join(pos, " ")
		hits := twddocs.Search(q, *limit)
		if hits == nil {
			hits = []twddocs.Hit{}
		}
		type hit struct {
			Ref     string `json:"ref"`
			Doc     string `json:"doc"`
			Heading string `json:"heading"`
			Excerpt string `json:"excerpt"`
			URL     string `json:"url"`
			Score   int    `json:"score"`
		}
		out := make([]hit, 0, len(hits))
		for _, h := range hits {
			out = append(out, hit{Ref: h.Ref(), Doc: h.Doc, Heading: h.Heading, Excerpt: h.Excerpt, Score: h.Score,
				URL: "https://github.com/rosskukulinski/twd/blob/master/" + twddocs.RepoPath(h.Ref())})
		}
		emit(f, map[string]any{"query": q, "results": out}, func() {
			if len(out) == 0 {
				fmt.Printf("no section mentions %q · twd docs ls\n", q)
			}
			for _, h := range out {
				fmt.Printf("%s\n  %s\n", h.Ref, h.Excerpt)
			}
			if len(out) > 0 {
				fmt.Println("\nread one: twd docs show <ref>")
			}
		})
		return nil
	}
	return fail("bad_request", "unknown subcommand docs "+sub, "usage: twd "+usageOf("docs"))
}
