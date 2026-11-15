package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestShowAndSearch(t *testing.T) {
	if s, ok := Show("errors.md#acl_denied"); !ok || !strings.HasPrefix(s, "### acl_denied\n") || strings.Contains(s, "### already_joined") {
		t.Fatalf("Show(errors.md#acl_denied) = %q, %v", s, ok)
	}
	if _, ok := Show("docs/twd-jsonc.md#tags-and-twd-init"); !ok {
		t.Fatal("Show with a docs/ prefix and a slug anchor")
	}
	if _, ok := Show("errors.md#no-such-code"); ok {
		t.Fatal("Show found a missing anchor")
	}
	hits := Search("acl_denied", 5)
	if len(hits) == 0 || hits[0].Ref() != "errors.md#acl_denied" {
		t.Fatalf("Search(acl_denied) best hit: %+v", hits)
	}
	if s, ok := Show("../README.md#quick-start-deploy-your-first-durable-object"); !ok || !strings.Contains(s, "twd deploy") {
		t.Fatalf("Show(../README.md#quick-start…) = %q, %v", s, ok)
	}
	if _, ok := Show("../go.mod"); ok {
		t.Fatal("Show read a repo-root file that isn't a doc")
	}
	if Search("zzz-no-such-word", 5) != nil {
		t.Fatal("Search found a word that isn't there")
	}
}

// llms.txt (repo root) indexes the docs for agents: every link resolves,
// and every user-facing doc is listed.
func TestLLMsTxt(t *testing.T) {
	b, err := os.ReadFile("../llms.txt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "# twd\n") {
		t.Fatal("llms.txt must start with `# twd`")
	}
	base := "https://github.com/rosskukulinski/twd/blob/master/"
	linked := map[string]bool{}
	for _, m := range regexp.MustCompile(`\]\(([^)]+)\)`).FindAllStringSubmatch(s, -1) {
		p, ok := strings.CutPrefix(m[1], base)
		if !ok {
			t.Errorf("llms.txt link %s: want %s<path>", m[1], base)
			continue
		}
		p, _, _ = strings.Cut(p, "#")
		if _, err := os.Stat(filepath.Join("..", p)); err != nil {
			t.Errorf("llms.txt links %s, which doesn't exist", p)
		}
		linked[p] = true
	}
	for _, d := range List() {
		if strings.HasPrefix(d.Path, "design/adr/") && d.Path != "design/adr/README.md" {
			continue // the ADR index links them
		}
		if !linked[RepoPath(d.Path)] {
			t.Errorf("llms.txt doesn't list %s", RepoPath(d.Path))
		}
	}
}

// TestSearchRanksGuides: a word that names a guide finds that guide first,
// not the API contract or a design record that mentions it.
func TestSearchRanksGuides(t *testing.T) {
	for q, want := range map[string]string{"secret": "secrets.md", "kv": "kv.md", "logs": "observability.md", "upgrade": "upgrading.md", "public webhook": "public-ingress.md", "quick start": "../README.md", "changelog": "../CHANGELOG.md"} {
		hits := Search(q, 3)
		found := false
		for _, h := range hits {
			found = found || h.Doc == want
		}
		if !found || strings.HasPrefix(hits[0].Doc, "contracts/") || strings.HasPrefix(hits[0].Doc, "design/") {
			var got []string
			for _, h := range hits {
				got = append(got, h.Ref())
			}
			t.Errorf("search %q: want %s in the top 3 and a guide first, got %v", q, want, got)
		}
	}
}
