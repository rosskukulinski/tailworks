package main

// tw docs (issue #25): tailworks' docs, embedded in the binary, for people and
// coding agents with no network or no repo checkout.
//
//	tw docs search <words…> [--limit 10]
//	tw docs show <doc>[#<anchor>]      e.g. errors.md#acl_denied, ../README.md
//	tw docs ls

import (
	"fmt"
	"strings"

	repodocs "github.com/rosskukulinski/tailworks/docs"
)

func cmdDocs(args []string) error {
	sub, args, ok := splitSubcommand(args) // agent.go
	if !ok {
		return needsSubcommand("docs")
	}
	sub = normalizeVerb(sub)
	f := newFlags("docs")
	limit := new(int)
	if sub == "search" {
		limit = f.Int("limit", 10, "at most this many sections")
	}
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	switch sub {
	case "ls":
		ds := repodocs.List()
		emit(f, map[string]any{"docs": ds}, func() {
			for _, d := range ds {
				fmt.Printf("  %-44s %s\n", d.Path, d.Title)
			}
		})
		return nil
	case "show":
		if len(pos) != 1 {
			return fail("bad_request", "tw docs show needs one doc, e.g. errors.md#acl_denied", "usage: tw "+usageOf("docs"))
		}
		md, ok := repodocs.Show(pos[0])
		if !ok {
			if u := repodocs.OnGitHubOnly(pos[0]); u != "" {
				return fail("not_found", pos[0]+" isn't built into the CLI: the API contract and the design records are on GitHub").withFix(u)
			}
			return fail("not_found", "no doc or section "+pos[0]).withFix("tw docs ls", "tw docs search <words>")
		}
		emit(f, map[string]any{"ref": pos[0], "markdown": md}, func() { fmt.Print(md) })
		return nil
	case "search":
		if len(pos) == 0 {
			return fail("bad_request", "tw docs search needs words to look for", "usage: tw "+usageOf("docs"))
		}
		q := strings.Join(pos, " ")
		hits := repodocs.Search(q, *limit)
		if hits == nil {
			hits = []repodocs.Hit{}
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
				URL: repodocs.GitHubBlob + repodocs.RepoPath(h.Ref())})
		}
		emit(f, map[string]any{"query": q, "results": out}, func() {
			if len(out) == 0 {
				fmt.Printf("no section mentions %q · tw docs ls\n", q)
			}
			for _, h := range out {
				fmt.Printf("%s\n  %s\n", h.Ref, h.Excerpt)
			}
			if len(out) > 0 {
				fmt.Println("\nread one: tw docs show <ref>")
			}
		})
		return nil
	}
	return fail("bad_request", "unknown subcommand docs "+sub, "usage: tw "+usageOf("docs"))
}
