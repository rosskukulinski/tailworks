package esbundle

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRuleGlobs(t *testing.T) {
	rules := EffectiveRules([]Rule{{Type: "Text", Globs: []string{"**/*.md"}, Fallthrough: true}})
	for spec, want := range map[string]string{
		"./doc.md":            "Text",
		"../a/b/doc.md":       "Text",
		"doc.md":              "Text",
		"./hello.txt":         "Text",
		"./page.html":         "Text",
		"./q.sql":             "Text",
		"./blob.bin":          "Data",
		"./m.wasm":            "CompiledWasm",
		"./m.wasm?module":     "CompiledWasm",
		"./m.wasmx":           "",
		"./index.ts":          "",
		"./doc.mdx":           "",
		"pkg/dist/module.bin": "Data",
	} {
		got, _ := MatchRule(rules, spec)
		if got != want {
			t.Errorf("%s: %q, want %q", spec, got, want)
		}
	}
}

func TestEffectiveRules(t *testing.T) {
	// A Text rule without fallthrough replaces wrangler's default Text rule
	// (.txt/.html/.sql); Data and CompiledWasm defaults stay.
	rs := EffectiveRules([]Rule{{Type: "Text", Globs: []string{"**/*.md"}}})
	if _, ok := MatchRule(rs, "./a.txt"); ok {
		t.Fatalf("default Text rule kept: %+v", rs)
	}
	if typ, _ := MatchRule(rs, "./a.wasm"); typ != "CompiledWasm" {
		t.Fatalf("wasm: %q", typ)
	}
	if len(EffectiveRules(nil)) != len(DefaultRules) {
		t.Fatal("no user rules: defaults only")
	}
}

// TestBuildAppWranglerSettings: define, alias and rules (a custom Text
// rule plus the defaults) in the built-in bundler, shaped like wrangler's
// output: additional modules named <sha1>-<base>, imported from the entry.
func TestBuildAppWranglerSettings(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"src/index.js": `import doc from "./doc.md"; import txt from "./t.txt"; import w from "./m.wasm"; import { lib } from "my-lib";
export default { fetch() { return Response.json({ b: BUILD_ID, doc, txt, w: !!w, lib: lib() }); } };`,
		"src/lib.js": `export const lib = () => "aliased";`,
		"src/doc.md": "# doc\n",
		"src/t.txt":  "text\n",
		"src/m.wasm": "\x00asm\x01\x00\x00\x00",
	}
	for p, c := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	out := filepath.Join(t.TempDir(), "index.js")
	r, err := BuildApp(Options{
		Dir: dir, Main: "src/index.js", Outfile: out,
		Define: map[string]string{"BUILD_ID": `"b42"`},
		Alias:  map[string]string{"my-lib": "./src/lib.js"},
		Rules:  []Rule{{Type: "Text", Globs: []string{"**/*.md"}, Fallthrough: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	js, _ := os.ReadFile(out)
	want := map[string]string{
		HashedName("doc.md", []byte(files["src/doc.md"])): "text",
		HashedName("t.txt", []byte(files["src/t.txt"])):   "text",
		HashedName("m.wasm", []byte(files["src/m.wasm"])): "wasm",
	}
	if len(r.Modules) != len(want) {
		t.Fatalf("modules %v", r.Modules)
	}
	for name, typ := range want {
		m, ok := r.Modules[name]
		if !ok || m.Type != typ {
			t.Fatalf("module %s: %+v (have %v)", name, m, r.Modules)
		}
		if !strings.Contains(string(js), `from "./`+name+`"`) {
			t.Fatalf("entry does not import ./%s:\n%s", name, js)
		}
	}
	if !strings.Contains(string(js), `"b42"`) || !strings.Contains(string(js), `"aliased"`) {
		t.Fatalf("define/alias not applied:\n%s", js)
	}
	if regexp.MustCompile(`BUILD_ID`).Match(js) {
		t.Fatalf("BUILD_ID left in output")
	}
	if r.Size != len(js) {
		t.Fatalf("size %d, file %d", r.Size, len(js))
	}
}

func TestBuildAppUnsupportedRule(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "i.js"), []byte("export default {}"), 0o644)
	_, err := BuildApp(Options{Dir: dir, Main: "i.js", Outfile: filepath.Join(dir, "o.js"), Rules: []Rule{{Type: "CommonJS", Globs: []string{"**/*.cjs"}}}})
	if err == nil || !strings.Contains(err.Error(), "--wrangler") {
		t.Fatalf("CommonJS rule: %v", err)
	}
}
