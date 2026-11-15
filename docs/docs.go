// Package docs embeds twd's Markdown docs in the twd binary, so
// `twd docs search|show|ls` (and coding agents) can read them offline, at
// the version of the CLI that runs. The repo root's llms.txt indexes the
// same files for agents that read the repo.
package docs

import (
	"embed"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.md contracts/*.md design/RFC.md design/adr/*.md
var files embed.FS

// Doc is one embedded doc.
type Doc struct {
	Path  string `json:"path"` // relative to docs/, e.g. "errors.md"
	Title string `json:"title"`
}

// Section is a heading and the text under it, up to the next heading.
type Section struct {
	Doc     string `json:"doc"`
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	Anchor  string `json:"anchor"`
	Text    string `json:"-"`
}

// Ref is doc#anchor.
func (s Section) Ref() string {
	if s.Anchor == "" {
		return s.Doc
	}
	return s.Doc + "#" + s.Anchor
}

// List returns every doc, sorted by path.
func List() []Doc {
	var out []Doc
	fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		b, _ := files.ReadFile(p)
		out = append(out, Doc{Path: p, Title: title(string(b), p)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func title(s, p string) string {
	for _, l := range strings.Split(s, "\n") {
		if t, ok := strings.CutPrefix(l, "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return path.Base(p)
}

// Read returns a doc's Markdown. name may carry a "docs/" prefix.
func Read(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean(strings.TrimPrefix(name, "./")), "docs/")
	b, err := files.ReadFile(name)
	return string(b), err == nil
}

var anchorStrip = regexp.MustCompile(`[^\p{L}\p{N} _-]`)

// Anchor is GitHub's heading slug: lowercase, punctuation dropped, spaces
// to hyphens.
func Anchor(heading string) string {
	s := strings.ToLower(strings.TrimSpace(heading))
	s = anchorStrip.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, " ", "-")
}

// Sections splits a doc at its headings (outside code fences). Text before
// the first heading is a level-0 section with no anchor.
func Sections(doc string) []Section {
	md, ok := Read(doc)
	if !ok {
		return nil
	}
	var out []Section
	cur := Section{Doc: doc}
	var body []string
	fence := false
	seen := map[string]int{}
	flush := func() {
		cur.Text = strings.Join(body, "\n")
		if cur.Heading != "" || strings.TrimSpace(cur.Text) != "" {
			out = append(out, cur)
		}
	}
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		}
		lvl := 0
		if !fence {
			for lvl < len(l) && l[lvl] == '#' {
				lvl++
			}
		}
		if lvl > 0 && lvl <= 6 && len(l) > lvl && l[lvl] == ' ' {
			flush()
			h := strings.TrimSpace(l[lvl:])
			a := Anchor(h)
			if n := seen[a]; n > 0 {
				seen[a]++
				a = a + "-" + itoa(n)
			} else {
				seen[a] = 1
			}
			cur, body = Section{Doc: doc, Heading: h, Level: lvl, Anchor: a}, []string{l}
			continue
		}
		body = append(body, l)
	}
	flush()
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Show returns a whole doc, or with "#anchor" that section and its
// subsections.
func Show(ref string) (string, bool) {
	doc, anchor, _ := strings.Cut(ref, "#")
	doc = strings.TrimPrefix(path.Clean(strings.TrimPrefix(doc, "./")), "docs/")
	md, ok := Read(doc)
	if !ok || anchor == "" {
		return md, ok
	}
	secs := Sections(doc)
	for i, s := range secs {
		if s.Anchor != strings.ToLower(anchor) {
			continue
		}
		parts := []string{s.Text}
		for _, t := range secs[i+1:] {
			if t.Level <= s.Level {
				break
			}
			parts = append(parts, t.Text)
		}
		return strings.TrimRight(strings.Join(parts, "\n"), "\n") + "\n", true
	}
	return "", false
}

// Hit is one search result.
type Hit struct {
	Section
	Score   int    `json:"score"`
	Excerpt string `json:"excerpt"`
}

// Search returns the sections that contain every word of query (case
// insensitive), best first: words in the heading count most.
func Search(query string, limit int) []Hit {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	var hits []Hit
	for _, d := range List() {
		docName := strings.ToLower(strings.TrimSuffix(path.Base(d.Path), ".md") + " " + d.Title)
		for _, s := range Sections(d.Path) {
			h, t := strings.ToLower(s.Heading), strings.ToLower(s.Text)
			score := 0
			for _, term := range terms {
				n := strings.Count(t, term)
				if n == 0 {
					score = 0
					break
				}
				score += min(n, 10) + 10*strings.Count(h, term)
				if h == term || s.Anchor == term {
					score += 50
				}
				// The doc is about the term (secrets.md for "secret"):
				// its sections beat passing mentions elsewhere.
				if strings.Contains(docName, stem(term)) {
					score += 25
				}
			}
			if score > 0 {
				score = score * docWeight(d.Path) / 4
				hits = append(hits, Hit{Section: s, Score: score, Excerpt: excerpt(s.Text, terms)})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Ref() < hits[j].Ref()
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// stem drops a plural or verb ending, for matching doc names only
// ("upgrade" finds upgrading.md, "secrets" finds secrets.md).
func stem(term string) string {
	for _, suf := range []string{"ing", "es", "s", "e"} {
		if len(term) > len(suf)+3 && strings.HasSuffix(term, suf) {
			return strings.TrimSuffix(term, suf)
		}
	}
	return term
}

// docWeight ranks the user guides above the API contract and the design
// records (in quarters): "secret" should find secrets.md before the
// mgmt API's secret operations or an ADR.
func docWeight(p string) int {
	if strings.HasPrefix(p, "contracts/") || strings.HasPrefix(p, "design/") {
		return 2
	}
	return 4
}

// excerpt is the first non-heading line with a query word (else the
// first non-heading line), trimmed.
func excerpt(text string, terms []string) string {
	first := ""
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if first == "" {
			first = t
		}
		ll := strings.ToLower(t)
		for _, term := range terms {
			if i := strings.Index(ll, term); i >= 0 {
				r := []rune(t)
				start := max(0, len([]rune(t[:i]))-60)
				end := min(len(r), start+180)
				out := string(r[start:end])
				if start > 0 {
					out = "…" + out
				}
				if end < len(r) {
					out += "…"
				}
				return out
			}
		}
	}
	if r := []rune(first); len(r) > 180 {
		return string(r[:180]) + "…"
	}
	return first
}
