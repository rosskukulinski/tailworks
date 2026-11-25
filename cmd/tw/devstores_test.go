package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevKVPersists: `tw dev` keeps a KV namespace between runs (like
// wrangler dev's local state), in <app>/.tailworks (git-ignored, owner-only).
func TestDevKVPersists(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "kvdev")
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "kvdev", "tailnet": { "tags": ["tag:tw-wl-kvdev"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "kvdev", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"kv_namespaces": [{ "binding": "KV", "id": "counter" }]}`), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`export default { async fetch(req, env) {
		const n = Number(await env.KV.get("n") ?? 0) + 1;
		await env.KV.put("n", String(n));
		return Response.json({ n });
	} };`), 0o644)
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	for want := 1; want <= 2; want++ {
		var r struct {
			Smoke map[string]any `json:"smoke"`
		}
		e.MustJSON(&r, "dev", app, "--once", "--port", "0")
		if r.Smoke["n"] != float64(want) {
			t.Fatalf("run %d: %+v, want n=%d (the namespace kept between tw dev runs)", want, r.Smoke, want)
		}
	}
	st, err := os.Stat(filepath.Join(app, ".tailworks", "dev-stores.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("dev-stores.json: %v %v", st, err)
	}
	if b, err := os.ReadFile(filepath.Join(app, ".tailworks", ".gitignore")); err != nil || string(b) != "# tw dev local state\n*\n" {
		t.Fatalf(".tailworks/.gitignore: %q %v", b, err)
	}
}
