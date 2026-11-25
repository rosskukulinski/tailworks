package gen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestGenerateKV: a kv_namespaces binding is workerd's kvNamespace binding
// targeting the platform's KVBackend with props naming the namespace; the
// platform holds a native binding of it for the facet proxy (KVFacet).
func TestGenerateKV(t *testing.T) {
	app := &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Modules: map[string][]byte{"index.js": []byte("export default { fetch() { return new Response('ok'); } };")},
		Classes: map[string]*bundle.Class{},
		Stores:  []bundle.StoreBinding{{Kind: "kv", Binding: "OAUTH_KV", Name: "oauth"}},
	}
	dir := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	c := string(cfg)
	props := `props = (json = "{\"kind\":\"kv\",\"name\":\"oauth\"}")`
	for _, want := range []string{
		`(name = "OAUTH_KV", kvNamespace = (name = "platform", entrypoint = "KVBackend", ` + props + `))`,
		`(name = "__TW_STORE_OAUTH_KV", kvNamespace = (name = "platform", entrypoint = "KVBackend", ` + props + `))`,
		`(name = "tw-store-kv.js", esModule = embed "tw-store-kv.js")`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "tw-stores.js"))
	if !strings.Contains(string(idx), `export { KVBackend, KVFacet } from "tw-store-kv.js";`) {
		t.Errorf("tw-stores.js: %s", idx)
	}
	var tc struct {
		Stores []struct {
			Binding, Facet string
			Props          map[string]string
		} `json:"stores"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tw-config.json"))
	if err := json.Unmarshal(b, &tc); err != nil || len(tc.Stores) != 1 || tc.Stores[0].Facet != "KVFacet" || tc.Stores[0].Props["binding"] != "OAUTH_KV" {
		t.Fatalf("tw-config.json stores: %+v %v", tc.Stores, err)
	}
}
