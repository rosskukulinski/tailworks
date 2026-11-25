package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vers(es []Entry) string {
	var v []string
	for _, e := range es {
		v = append(v, e.Version)
	}
	return strings.Join(v, ",")
}

func TestParse(t *testing.T) {
	one, err := Parse([]byte(`{"version":"0.2.0","notes":[]}`))
	if err != nil || len(one) != 1 || one[0].Version != "0.2.0" {
		t.Fatalf("object: %v %v", one, err)
	}
	arr, err := Parse([]byte(` [{"version":"0.3.0"},{"version":"v0.2.0"}]`))
	if err != nil || len(arr) != 2 {
		t.Fatalf("array: %v %v", arr, err)
	}
	if _, err := Parse([]byte(`{"version":"next"}`)); err == nil {
		t.Fatal("bad version accepted")
	}
	if _, err := Parse([]byte(`{`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestBetweenAndSteps(t *testing.T) {
	es := []Entry{{Version: "0.4.0"}, {Version: "0.2.0", ContractMigrations: []string{"202611200000_drop_x"}},
		{Version: "0.1.0"}, {Version: "0.3.0", Notes: []Note{{Kind: KindBreaking, Text: "b"}, {Kind: KindFix}}}, {Version: "0.2.1"}}
	if got := vers(Between(es, "0.1.0", "0.3.0")); got != "0.2.0,0.2.1,0.3.0" {
		t.Errorf("Between = %s", got)
	}
	if got := vers(Between(es, "", "0.2.0")); got != "0.1.0,0.2.0" {
		t.Errorf("Between from start = %s", got)
	}
	if got := vers(Between(es, "0.4.0", "0.4.0")); got != "" {
		t.Errorf("Between same = %s", got)
	}
	if got := vers(ContractSteps(Between(es, "0.1.0", "0.4.0"))); got != "0.2.0" {
		t.Errorf("ContractSteps = %s", got)
	}
	if b := Breaking(es); len(b) != 1 || b[0].Text != "b" {
		t.Errorf("Breaking = %v", b)
	}
	if e, ok := Find(es, "v0.2.1"); !ok || e.Version != "0.2.1" {
		t.Errorf("Find = %v %v", e, ok)
	}
}

func TestEmbedded(t *testing.T) {
	es := Embedded()
	if len(es) == 0 || es[0].Version != "0.1.0" {
		t.Fatalf("Embedded = %s", vers(es))
	}
	for i := 1; i < len(es); i++ {
		if cmp(es[i-1].Version, es[i].Version) >= 0 {
			t.Fatalf("not ascending: %s", vers(es))
		}
	}
	// Every release file is named after its version.
	files, _ := filepath.Glob("releases/*.json")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		got, err := Parse(b)
		if err != nil || len(got) != 1 || "v"+got[0].Version+".json" != filepath.Base(f) {
			t.Errorf("%s: %v %v", f, got, err)
		}
	}
}

func TestParseFragment(t *testing.T) {
	n, err := ParseFragment("b.md", []byte("---\nkind: fix\nupgrade: [format, protocol]\n---\nFixed it.\n\nOperator action: restart.\n"))
	if err != nil || n.Kind != KindFix || len(n.Upgrade) != 2 || !n.IsAction() || n.Source != "b.md" {
		t.Fatalf("%+v %v", n, err)
	}
	n, err = ParseFragment("c.md", []byte("---\nkind: feature\nupgrade: []\n---\nNew.\n\nOperator action: none.\n"))
	if err != nil || len(n.Upgrade) != 0 || n.IsAction() {
		t.Fatalf("%+v %v", n, err)
	}
	if n, _ := ParseFragment("w.md", []byte("---\nkind: feature\nupgrade: [workerd]\n---\nBump.\n")); !n.IsAction() {
		t.Error("workerd bump is an upgrade action")
	}
	for name, bad := range map[string]string{
		"no front matter": "kind: fix\n",
		"unclosed":        "---\nkind: fix\nupgrade: []\n",
		"bad kind":        "---\nkind: chore\nupgrade: []\n---\nx\n",
		"bad upgrade":     "---\nkind: fix\nupgrade: [schema]\n---\nx\n",
		"not a list":      "---\nkind: fix\nupgrade: format\n---\nx\n",
		"no upgrade":      "---\nkind: fix\n---\nx\n",
		"no kind":         "---\nupgrade: []\n---\nx\n",
		"unknown key":     "---\nkind: fix\nupgrade: []\nissue: 3\n---\nx\n",
		"empty body":      "---\nkind: fix\nupgrade: []\n---\n\n",
	} {
		if _, err := ParseFragment("x.md", []byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMarkdownSections(t *testing.T) {
	md := Markdown("tw v0.2.0", []Note{
		{Kind: KindFeature, Source: "f.md", Text: "feat"},
		{Kind: KindBreaking, Source: "b.md", Text: "broke"},
		{Kind: KindFix, Source: "x.md", Text: "fixed", Upgrade: []string{"schema-contract"}},
		{Kind: KindSecurity, Source: "s.md", Text: "sec"},
	})
	order := []string{"# tw v0.2.0", "## Breaking", "### b", "## Security", "## Upgrade actions", "### x", "## Features", "## Fixes"}
	last := -1
	for _, h := range order {
		i := strings.Index(md[last+1:], h)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", h, md)
		}
		last += i + 1
	}
	if got := Union([]Note{{Upgrade: []string{"protocol", "format"}}, {Upgrade: []string{"format"}}}); strings.Join(got, ",") != "format,protocol" {
		t.Errorf("Union = %v", got)
	}
}

// The repo's own fragments must all be valid (check-changes.sh also checks).
func TestRepoFragments(t *testing.T) {
	files, _ := filepath.Glob("../../changes/*.md")
	rel, _ := filepath.Glob("../../changes/released/*/*.md")
	for _, f := range append(files, rel...) {
		if filepath.Base(f) == "README.md" {
			continue
		}
		b, _ := os.ReadFile(f)
		if _, err := ParseFragment(filepath.Base(f), b); err != nil {
			t.Error(err)
		}
	}
}
