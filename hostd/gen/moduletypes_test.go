package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestGenerateModuleTypes: a module the bundler typed (a wrangler Text rule
// on *.md) is a text module of the tenant worker, and text for the facets'
// Dynamic Worker too; untyped modules keep their extension's type.
func TestGenerateModuleTypes(t *testing.T) {
	app := &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Modules: map[string][]byte{
			"index.js":    []byte(`import n from "./aa-notes.md"; export default { fetch() { return new Response(n); } };`),
			"aa-notes.md": []byte("# notes"),
			"q.bin":       []byte{1, 2},
		},
		ModuleTypes: map[string]string{"aa-notes.md": "text"},
		Classes:     map[string]*bundle.Class{},
	}
	dir := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	for _, want := range []string{`(name = "aa-notes.md", text = embed "t/aa-notes.md")`, `(name = "q.bin", data = embed "t/q.bin")`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	tm, _ := os.ReadFile(filepath.Join(dir, "tw-modules.js"))
	if !strings.Contains(string(tm), `"aa-notes.md": { text: `) {
		t.Errorf("tw-modules.js does not type the .md as text:\n%s", tm)
	}
}
