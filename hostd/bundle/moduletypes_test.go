package bundle

import (
	"strings"
	"testing"
)

// TestModuleTypes: tw-bundle.json "modules" types a module whatever its
// extension (wrangler rules: a Text module named *.md, a .sql text module),
// and ships files whose extension would otherwise be dropped.
func TestModuleTypes(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc":   []byte(`{"name":"w","main":"src/index.ts"}`),
		"tw-bundle.json":   []byte(`{"main":"dist/index.js","modules":{"dist/aa-notes.md":"text","dist/bb-q.sql":"text","dist/cc-m.wasm":"wasm"}}`),
		"dist/index.js":    []byte("export default {}"),
		"dist/aa-notes.md": []byte("# notes"),
		"dist/bb-q.sql":    []byte("select 1"),
		"dist/cc-m.wasm":   []byte("\x00asm\x01\x00\x00\x00"),
		"dist/untyped.md":  []byte("dropped"),
		"dist/plain.txt":   []byte("text by extension"),
	}
	a, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"dist/aa-notes.md": "text", "dist/bb-q.sql": "text", "dist/cc-m.wasm": "wasm", "dist/plain.txt": "text", "dist/index.js": "js"} {
		if _, ok := a.Modules[p]; !ok {
			t.Errorf("%s not shipped", p)
		}
		if got := a.ModuleKind(p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	if _, ok := a.Modules["dist/untyped.md"]; ok {
		t.Error("an untyped .md was shipped")
	}

	for name, tb := range map[string]string{
		"bad type": `{"main":"dist/index.js","modules":{"dist/aa-notes.md":"commonjs"}}`,
		"missing":  `{"main":"dist/index.js","modules":{"dist/nope.md":"text"}}`,
	} {
		f := map[string][]byte{}
		for k, v := range files {
			f[k] = v
		}
		f["tw-bundle.json"] = []byte(tb)
		if _, err := Parse(f); err == nil || !strings.Contains(err.Error(), "tw-bundle.json modules") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
