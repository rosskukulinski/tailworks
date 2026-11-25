package compatdoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRoot is a tiny repo: one Go test package and one JS test file.
func fixtureRoot(t *testing.T) string {
	root := t.TempDir()
	write := func(p, s string) {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg/a_test.go", "//go:build sdk\n\npackage pkg\n\nfunc TestThing(t *testing.T) {\n\tt.Run(\"Sub\", nil)\n\tt.Run(\"Loop/\"+k, nil)\n}\n")
	write("js/x.test.js", "test(\"does a thing\", () => {});\n")
	return root
}

const readmeOK = "# x\n\n## Compatibility at a glance\n\n| Area | Surface | Status | Note |\n|---|---|---|---|\n" +
	"| Runtime | [Fetch](docs/COMPAT.md#fetch) | ✅ | |\n" +
	"| Bindings | [KV](docs/COMPAT.md#kv) | ❌ | |\n" +
	"| Bindings | [Secrets](docs/COMPAT.md#secrets) | ⚠️ | |\n" +
	"\n## More\n\n| [ignored](docs/COMPAT.md#nope) | ✅ |\n"

const compatOK = "# c\n\n| Surface | Status | provides | Tests |\n|---|---|---|---|\n" +
	"| <a id=\"fetch\"></a>fetch | ✅ | a \\| b | `pkg:TestThing/Sub`, `pkg:TestThing/Loop`, `js/x.test.js:does a thing` |\n" +
	"| <a id=\"kv\"></a>KV | ❌ | — | — |\n" +
	"| <a id=\"secrets\"></a>Secrets | ⚠ | — | `pkg:TestThing` (nightly) |\n" +
	"| not a surface | ✅ | | |\n"

func TestParse(t *testing.T) {
	rows, errs := ParseCompat([]byte(compatOK))
	if len(errs) != 0 || len(rows) != 3 {
		t.Fatalf("%v %+v", errs, rows)
	}
	if rows[0].ID != "fetch" || rows[0].Status != Works || len(rows[0].Tests) != 3 || rows[0].Tests[2] != "js/x.test.js:does a thing" {
		t.Fatalf("%+v", rows[0])
	}
	if rows[2].Status != Partial {
		t.Fatalf("⚠ without the variation selector: %+v", rows[2])
	}
	rr, errs := ParseReadme([]byte(readmeOK))
	if len(errs) != 0 || len(rr) != 3 || rr[2].ID != "secrets" || rr[2].Status != Partial {
		t.Fatalf("%v %+v", errs, rr)
	}
}

func TestCheckOK(t *testing.T) {
	if errs := CheckDocs(fixtureRoot(t), []byte(readmeOK), []byte(compatOK)); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestCheckFindsDrift(t *testing.T) {
	root := fixtureRoot(t)
	for _, c := range []struct {
		name, readme, compat, want string
	}{
		{"ReadmeRowWithoutDetail", readmeOK + "", strings.Replace(compatOK, `<a id="kv"></a>`, "", 1),
			`docs/COMPAT.md#kv has no surface row`},
		{"DetailWithoutReadmeRow", readmeOK, compatOK + "| <a id=\"d1\"></a>D1 | ❌ | | |\n",
			`surface "d1" has no row in README.md`},
		{"StatusMismatch", strings.Replace(readmeOK, "[KV](docs/COMPAT.md#kv) | ❌", "[KV](docs/COMPAT.md#kv) | ✅", 1), compatOK,
			`"kv" is ✅ here but ❌ in docs/COMPAT.md`},
		{"WorksWithoutTest", readmeOK, strings.Replace(compatOK, "`pkg:TestThing`", "—", 1),
			`surface "secrets" is ⚠️ but names no test`},
		{"MissingTestFunc", readmeOK, strings.Replace(compatOK, "`pkg:TestThing`", "`pkg:TestOther`", 1),
			`no func TestOther(t *testing.T) in pkg/*_test.go`},
		{"MissingSubtest", readmeOK, strings.Replace(compatOK, "TestThing/Sub", "TestThing/Gone", 1),
			`subtest "Gone" of TestThing is not in pkg/*_test.go`},
		{"MissingPath", readmeOK, strings.Replace(compatOK, "`pkg:TestThing`", "`nope:TestThing`", 1),
			`nope does not exist`},
		{"MissingJSTest", readmeOK, strings.Replace(compatOK, "does a thing", "does another", 1),
			`"does another" is not in js/x.test.js`},
		{"BadStatus", strings.Replace(readmeOK, "[KV](docs/COMPAT.md#kv) | ❌", "[KV](docs/COMPAT.md#kv) | no", 1), strings.Replace(compatOK, "KV | ❌", "KV | no", 1),
			`status must start with one of`},
		{"Duplicate", readmeOK, compatOK + "| <a id=\"kv\"></a>KV again | ❌ | | |\n",
			`surface "kv" is already defined`},
		{"NoSection", "# x\n", compatOK, `no "## Compatibility at a glance" section`},
	} {
		t.Run(c.name, func(t *testing.T) {
			errs := CheckDocs(root, []byte(c.readme), []byte(c.compat))
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want an error containing %q, got:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}

// TestRepoDocs runs the check on this repo's README.md and docs/COMPAT.md
// (make lint runs the same check through ./internal/compatdoc/compatcheck).
func TestRepoDocs(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range Check(root) {
		t.Error(e)
	}
	b, _ := os.ReadFile(filepath.Join(root, "README.md"))
	rows, _ := ParseReadme(b)
	if len(rows) < 30 {
		t.Fatalf("README compatibility table has %d rows; the parser is probably missing them", len(rows))
	}
}
