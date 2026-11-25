package bundle_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestParseKVNamespaces: kv_namespaces entries are "kv" stores named by
// their id, or by the binding without one; bindings may share an id.
func TestParseKVNamespaces(t *testing.T) {
	parse := func(kv string) (*bundle.App, error) {
		return bundle.Parse(map[string][]byte{
			"index.js":       []byte("export default {}"),
			"wrangler.jsonc": []byte(`{ "name": "w", "main": "index.js", "kv_namespaces": ` + kv + ` }`),
		})
	}
	a, err := parse(`[{ "binding": "OAUTH_KV", "id": "0f2ac74b498b48028cb68387c421e279", "preview_id": "x" },
		{ "binding": "CACHE" }, { "binding": "ALIAS", "id": "0f2ac74b498b48028cb68387c421e279" }]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []bundle.StoreBinding{
		{Kind: "kv", Binding: "OAUTH_KV", Name: "0f2ac74b498b48028cb68387c421e279"},
		{Kind: "kv", Binding: "CACHE", Name: "CACHE"},
		{Kind: "kv", Binding: "ALIAS", Name: "0f2ac74b498b48028cb68387c421e279"},
	}
	if len(a.Stores) != 3 || a.Stores[0] != want[0] || a.Stores[1] != want[1] || a.Stores[2] != want[2] {
		t.Fatalf("stores: %+v", a.Stores)
	}
	for kv, msg := range map[string]string{
		`[{ "id": "x" }]`:                          "no binding",
		`[{ "binding": "K", "id": "a/b" }]`:        "store name",
		`[{ "binding": "K" }, { "binding": "K" }]`: "another storage binding",
		`{}`: "kv_namespaces",
	} {
		if _, err := parse(kv); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want %q", kv, err, msg)
		}
	}
}
