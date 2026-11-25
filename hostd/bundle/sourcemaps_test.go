package bundle

import "testing"

// TestSourceMaps: a bundler's <module>.js.map is kept as a source map, not
// shipped to workerd as a module.
func TestSourceMaps(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc":                   []byte(`{"name":"w","main":"src/index.ts"}`),
		"tw-bundle.json":                   []byte(`{"main":"dist/index.js"}`),
		"dist/index.js":                    []byte("export default {}"),
		"dist/index.js.map":                []byte(`{"version":3,"sources":[],"mappings":""}`),
		"dist/other.css.map":               []byte("{}"),
		"node_modules/x/dist/index.js.map": []byte("{}"),
	}
	a, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SourceMaps["dist/index.js.map"]; !ok || len(a.SourceMaps) != 1 {
		t.Fatalf("source maps: %v", keys(a.SourceMaps))
	}
	for p := range a.Modules {
		if p != "dist/index.js" {
			t.Errorf("%s shipped as a module", p)
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
