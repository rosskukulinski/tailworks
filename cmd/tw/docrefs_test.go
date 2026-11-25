package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docRefGlobs are the docs whose `tw …` commands TestDocsNameRealCommands
// checks (issue #74), relative to the repo root. docs/cli.md is left out:
// it is generated from the flag sets (TestCLIReferenceUpToDate). The RFC
// and the numbered ADRs are in: they name today's commands, and say in
// words what was never built or was removed. An ADR drafted on a branch
// under a slug name (docs/design/adr/<slug>.md, AGENTS.md rule 5) is
// left out until it gets its number.
var docRefGlobs = []string{
	"README.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
	"docs/*.md",
	"docs/contracts/*.md",
	"docs/design/*.md",
	"docs/design/adr/[0-9][0-9][0-9]-*.md",
}

// docRefAllowed are commands the docs name on purpose although the CLI
// doesn't offer them: ones that answer not_supported, and ones that were
// removed (named so an upgrader recognises them).
var docRefAllowed = map[string]string{
	"tw r2 object get": "answers not_supported (r2.go)",
	"tw r2 object put": "answers not_supported (r2.go)",
	"tw up --wif":      "removed; docs/INSTALL.md explains the upgrade",
}

var docInlineCode = regexp.MustCompile("`([^`\n]+)`")

// docRef is one `tw …` command a doc names.
type docRef struct {
	file string
	line int
	args []string // the words after tw
}

func (r docRef) text() string { return strings.Join(append([]string{"tw"}, r.args...), " ") }

// docCmdStart: a word that may come right before a tw command in a shell
// line (`$ tw ls`, `cd x && tw dev`, `claude mcp add tw -- tw mcp`).
func docCmdStart(prev string) bool {
	switch prev {
	case "$", "&&", "||", "|", ";", "(", "--", "·", "exec", "sudo", "time", "then", "do", "or":
		return true
	}
	return strings.Contains(prev, "=") && !strings.HasPrefix(prev, "-") // FOO=1 tw …
}

// docCmdEnd: a word that ends a tw command.
func docCmdEnd(w string) bool {
	switch w {
	case "&&", "||", "|", ";", ")", "·", ">", ">>", "<", "2>&1", "or", "and":
		return true
	}
	return false
}

// docTwRefsIn returns the tw commands in one line of shell or one inline
// code span.
func docTwRefsIn(file string, line int, s string) []docRef {
	var out []docRef
	w := strings.Fields(strings.ReplaceAll(s, `\|`, "|")) // a table cell escapes |
	for i := 0; i < len(w); i++ {
		if w[i] != "tw" || i > 0 && !docCmdStart(w[i-1]) {
			continue
		}
		j := i + 1
		for j < len(w) && !docCmdEnd(w[j]) {
			j++
		}
		var args []string
		for _, a := range w[i+1 : j] {
			// `get|put`, `create|list`: alternatives. Check the first
			// spelling and stop there, since what follows may belong
			// to another one. A trailing comma or period is prose.
			first, _, alt := strings.Cut(a, "|")
			if first = strings.TrimRight(first, ".,:;)…"); first != "" {
				args = append(args, first)
			}
			if alt {
				break
			}
		}
		out = append(out, docRef{file: file, line: line, args: args})
		i = j - 1
	}
	return out
}

// docTwRefs returns every `tw <cmd> [--flag]` in md: inline code spans,
// and the lines of shell code blocks.
func docTwRefs(file, md string) []docRef {
	var out []docRef
	fence, shell := false, false
	for i, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			if !fence {
				switch strings.TrimPrefix(t, "```") {
				case "", "sh", "bash", "shell", "console", "zsh":
					shell = true
				default:
					shell = false
				}
			}
			fence = !fence
			continue
		}
		if fence {
			if shell {
				code, _, _ := strings.Cut(l, " #") // a trailing shell comment is prose
				out = append(out, docTwRefsIn(file, i+1, code)...)
			}
			continue
		}
		for _, m := range docInlineCode.FindAllStringSubmatch(l, -1) {
			out = append(out, docTwRefsIn(file, i+1, m[1])...)
		}
	}
	return out
}

// checkDocRef reports what in ref names no command, subcommand or flag.
func checkDocRef(tree *compTree, hidden map[string]bool, ref docRef) []string {
	a := ref.args
	if len(a) == 0 || docRefAllowed[ref.text()] != "" {
		return nil
	}
	if strings.HasPrefix(a[0], "-") {
		if fl := docFlag(a[0]); !docGlobalFlag(fl) && fl != "--version" && fl != "-v" {
			return []string{"no global flag " + fl}
		}
		return nil
	}
	if !compLiteral.MatchString(a[0]) || a[0] == "help" || hidden[a[0]] {
		return nil // a placeholder (`tw <command>`), `tw help …` or a hidden command
	}
	if tree.nodes[a[0]] == nil {
		return []string{"no command " + a[0]}
	}
	path := a[0]
	var bad []string
	positional := false // a word after path that isn't a subcommand
	for _, w := range a[1:] {
		if strings.HasPrefix(w, "-") {
			fl := docFlag(w)
			if fl == "" || docGlobalFlag(fl) {
				continue
			}
			if !tree.nodes[path].flags[fl] {
				bad = append(bad, "tw "+path+": no flag "+fl)
			}
			continue
		}
		next := joinPath(path, w)
		// Spellings the commands accept but their usage lines don't show
		// (wrangler's list and delete for ls and rm: normalizeVerb).
		if alias := joinPath(path, normalizeVerb(w)); tree.nodes[next] == nil && tree.nodes[alias] != nil {
			next = alias
		}
		if !positional && tree.nodes[next] != nil {
			path = next
			continue
		}
		// The first word after a command that has subcommands must be
		// one of them, unless it's a placeholder (<name>, …).
		if !positional && len(tree.nodes[path].subs) > 0 && compLiteral.MatchString(w) {
			bad = append(bad, "tw "+path+": no subcommand "+w)
		}
		positional = true
	}
	return bad
}

// docFlag is a flag as typed, without its =value (--tenant=x → --tenant);
// "" for what isn't a flag name (--, -, --<flag>).
func docFlag(w string) string {
	w, _, _ = strings.Cut(w, "=")
	if w == "--" || w == "-" || strings.ContainsAny(w, "<[{*") {
		return ""
	}
	return w
}

func docGlobalFlag(fl string) bool {
	switch fl {
	case "--help", "-h", "--json", "--state-dir":
		return true
	}
	return false
}

// TestDocsNameRealCommands: every `tw <cmd> [sub…] [--flag]` that
// README.md and docs/*.md name, in inline code or a shell code block, is
// a command, subcommand path and flag the CLI has (issue #74). It caught
// what the benchmark's manual "every command and flag the docs name
// exists" pass used to.
func TestDocsNameRealCommands(t *testing.T) {
	tree := buildCompletionTree()
	hidden := map[string]bool{}
	for _, c := range commands {
		if c.hidden {
			hidden[c.name] = true
		}
	}
	root := filepath.Join("..", "..")
	var files []string
	for _, g := range docRefGlobs {
		m, err := filepath.Glob(filepath.Join(root, g))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)
	n := 0
	for _, f := range files {
		if filepath.Base(f) == "cli.md" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, f)
		for _, ref := range docTwRefs(rel, string(b)) {
			n++
			for _, p := range checkDocRef(tree, hidden, ref) {
				t.Errorf("%s:%d: `%s`: %s", ref.file, ref.line, ref.text(), p)
			}
		}
	}
	// The docs name several hundred; far fewer means the extraction broke.
	if n < 300 {
		t.Fatalf("checked only %d tw commands in %d docs", n, len(files))
	}
}

// TestDocRefCheckFindsMistakes: the check flags a made-up command,
// subcommand and flag, and passes real ones.
func TestDocRefCheckFindsMistakes(t *testing.T) {
	tree := buildCompletionTree()
	md := "Run `tw deploy --no-wait` or `tw kv namespace ls`.\n" +
		"`tw connect foo`, `tw kv key frobnicate` and `tw deploy --frobnicate`.\n" +
		"```sh\n" +
		"tw ls --all                 # tw nope here is a comment\n" +
		"claude mcp add tw -- tw mcp --bogus\n" +
		"```\n"
	var got []string
	for _, ref := range docTwRefs("x.md", md) {
		got = append(got, checkDocRef(tree, nil, ref)...)
	}
	want := []string{
		"no command connect",
		"tw kv key: no subcommand frobnicate",
		"tw deploy: no flag --frobnicate",
		"tw mcp: no flag --bogus",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
