package gen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestGenerateStores: a storage binding becomes the platform's Store DO
// namespace, its modules, and a binding of the tenant worker targeting the
// kind's backend with props naming the store; facets get it through
// tw-config.json "stores". An app without stores gets none of it.
func TestGenerateStores(t *testing.T) {
	hostdtest.RegisterProbeStore()
	app := &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Modules: map[string][]byte{"index.js": []byte("export default { fetch() { return new Response('ok'); } };")},
		Classes: map[string]*bundle.Class{},
		Stores:  []bundle.StoreBinding{{Kind: "probe", Binding: "P", Name: "main"}, {Kind: "probe", Binding: "Q", Name: "other"}},
	}
	dir := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	c := string(cfg)
	for _, want := range []string{
		`(className = "Store", uniqueKey = "tw-w-__tw_store", enableSql = true)`,
		`(name = "tw-store.js", esModule = embed "tw-store.js")`,
		`(name = "tw-store-probe.js", esModule = embed "tw-store-probe.js")`,
		`(name = "P", service = (name = "platform", entrypoint = "ProbeBackend", props = (json = "{\"kind\":\"probe\",\"name\":\"main\"}")))`,
		`(name = "Q", service = (name = "platform", entrypoint = "ProbeBackend", props = (json = "{\"kind\":\"probe\",\"name\":\"other\"}")))`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	if strings.Contains(c, "__TW_STORE_") {
		t.Error("a service-binding kind needs no platform-side native binding")
	}
	pjs, _ := os.ReadFile(filepath.Join(dir, "platform.js"))
	if !strings.Contains(string(pjs), `export * from "tw-stores.js";`) {
		t.Error("platform.js does not export the stores")
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "tw-stores.js"))
	if !strings.Contains(string(idx), `export { ProbeBackend } from "tw-store-probe.js";`) {
		t.Errorf("tw-stores.js: %s", idx)
	}
	var tc struct {
		Stores []struct {
			Binding, Facet string
			Props          map[string]string
		} `json:"stores"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tw-config.json"))
	if err := json.Unmarshal(b, &tc); err != nil || len(tc.Stores) != 2 || tc.Stores[0].Facet != "ProbeBackend" || tc.Stores[0].Props["name"] != "main" {
		t.Fatalf("tw-config.json stores: %+v %v", tc.Stores, err)
	}

	// A secret may not take a storage binding's name; an unknown kind is refused.
	bad := *app
	bad.Secrets = map[string]string{"P": "x"}
	if _, err := gen.Generate(&bad, gen.Options{Dir: filepath.Join(t.TempDir(), "i"), DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err == nil {
		t.Error("a secret named like a storage binding was accepted")
	}
	bad = *app
	bad.Stores = []bundle.StoreBinding{{Kind: "nope", Binding: "P", Name: "main"}}
	if _, err := gen.Generate(&bad, gen.Options{Dir: filepath.Join(t.TempDir(), "i"), DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err == nil {
		t.Error("an unknown store kind was accepted")
	}

	// No stores: nothing of the substrate.
	plain := *app
	plain.Stores = nil
	dir2 := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(&plain, gen.Options{Dir: dir2, DOStorage: filepath.Join(dir2, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg2, _ := os.ReadFile(filepath.Join(dir2, "config.capnp"))
	if strings.Contains(string(cfg2), `className = "Store"`) || strings.Contains(string(cfg2), "tw-store") {
		t.Error("an app without stores got the Store namespace")
	}
}
