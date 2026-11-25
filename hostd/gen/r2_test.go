package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestGenerateR2: an r2_buckets binding is workerd's native r2Bucket
// binding targeting the platform's R2Backend with props naming the bucket;
// the platform holds a native binding of it for the facet proxy; facet
// classes get the R2 shim around their env (and nothing else changes for
// an app without R2).
func TestGenerateR2(t *testing.T) {
	app := &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Modules:  map[string][]byte{"index.js": []byte("export class Box {}; export default { fetch() { return new Response('ok'); } };")},
		Classes:  map[string]*bundle.Class{"Box": {Node: true}},
		Bindings: []bundle.DOBinding{{Name: "BOX", Class: "Box"}},
		Stores:   []bundle.StoreBinding{{Kind: "r2", Binding: "PHOTOS", Name: "photos"}},
	}
	gen1 := func(a *bundle.App) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "i")
		if _, err := gen.Generate(a, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir := gen1(app)
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	c := string(cfg)
	for _, want := range []string{
		`(name = "PHOTOS", r2Bucket = (name = "platform", entrypoint = "R2Backend", props = (json = "{\"kind\":\"r2\",\"name\":\"photos\"}")))`,
		`(name = "__TW_STORE_PHOTOS", r2Bucket = (name = "platform", entrypoint = "R2Backend", props = (json = "{\"kind\":\"r2\",\"name\":\"photos\"}")))`,
		`(name = "tw-store-r2.js", esModule = embed "tw-store-r2.js")`,
		`(name = "__tw_store_r2.js.txt", text = embed "__tw_store_r2.js")`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "tw-stores.js"))
	if !strings.Contains(string(idx), `export { R2Backend, R2FacetProxy } from "tw-store-r2.js";`) {
		t.Errorf("tw-stores.js: %s", idx)
	}
	facet, _ := os.ReadFile(filepath.Join(dir, "__tw_facet.js"))
	for _, want := range []string{
		`import { wrap as __tw_wrap_r2 } from "./__tw_store_r2.js";`,
		`"PHOTOS": __tw_wrap_r2,`,
		`export const Box = wrapFacetClass(__twStoreClass(T["Box"]), "Box", B);`,
	} {
		if !strings.Contains(string(facet), want) {
			t.Errorf("__tw_facet.js lacks %s:\n%s", want, facet)
		}
	}
	mods, _ := os.ReadFile(filepath.Join(dir, "tw-modules.js"))
	if !strings.Contains(string(mods), `"__tw_store_r2.js": { js: shim0 },`) || !strings.Contains(string(mods), `import shim0 from "__tw_store_r2.js.txt";`) {
		t.Errorf("tw-modules.js lacks the shim:\n%s", mods)
	}

	// No R2: no shim, facet classes as before.
	plain := *app
	plain.Stores = nil
	dir = gen1(&plain)
	facet, _ = os.ReadFile(filepath.Join(dir, "__tw_facet.js"))
	if strings.Contains(string(facet), "__twStore") || !strings.Contains(string(facet), `export const Box = wrapFacetClass(T["Box"], "Box", B);`) {
		t.Errorf("__tw_facet.js without R2:\n%s", facet)
	}
	if _, err := os.Stat(filepath.Join(dir, "__tw_store_r2.js")); err == nil {
		t.Error("shim written without an R2 binding")
	}
}
