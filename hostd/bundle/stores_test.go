package bundle_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestParseStores: registered storage binding keys become App.Stores, in
// wrangler order; bad binding or store names, and names that collide with
// a Durable Object binding, a var or another storage binding, are refused.
func TestParseStores(t *testing.T) {
	hostdtest.RegisterProbeStore()
	parse := func(probes string) (*bundle.App, error) {
		return bundle.Parse(map[string][]byte{
			"index.js": []byte("export default {}"),
			"wrangler.jsonc": []byte(`{ "name": "w", "main": "index.js", "vars": { "V": 1 },
				"durable_objects": { "bindings": [{ "name": "BOX", "class_name": "Box" }] },
				"` + hostdtest.ProbeStoreKey + `": ` + probes + ` }`),
		})
	}
	a, err := parse(`[{ "binding": "P", "name": "main" }, { "binding": "Q", "name": "main" }]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Stores) != 2 || a.Stores[0] != (bundle.StoreBinding{Kind: "probe", Binding: "P", Name: "main"}) || a.Stores[1].Binding != "Q" {
		t.Fatalf("stores: %+v", a.Stores)
	}
	for probes, want := range map[string]string{
		`[{ "binding": "BOX", "name": "x" }]`:                                "durable_objects binding",
		`[{ "binding": "V", "name": "x" }]`:                                  "a var",
		`[{ "binding": "P", "name": "x" }, { "binding": "P", "name": "y" }]`: "another storage binding",
		`[{ "binding": "1P", "name": "x" }]`:                                 "bad binding name",
		`[{ "binding": "__TW_X", "name": "x" }]`:                             "bad binding name",
		`[{ "binding": "P", "name": "a/b" }]`:                                "store name",
		`[{ "binding": "P", "name": "" }]`:                                   "store name",
		`{"no": "array"}`:                                                    "tw_test_probes",
	} {
		if _, err := parse(probes); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", probes, err, want)
		}
	}
	// Unregistered keys are ignored, as before.
	if a, err := bundle.Parse(map[string][]byte{"index.js": []byte(""), "wrangler.jsonc": []byte(`{"main":"index.js","r2_unknown":[1]}`)}); err != nil || len(a.Stores) != 0 {
		t.Fatalf("unknown key: %v %+v", err, a)
	}
}
