package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevVars: `tw dev` gives .dev.vars to the workload as secrets
// (env.NAME, overriding a wrangler var), through the same hostd path as
// `tw secret put`.
func TestDevVars(t *testing.T) {
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
	app := filepath.Join(work, "dv")
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "dv", "tailnet": { "tags": ["tag:tw-wl-dv"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "dv", "main": "src/index.js", "compatibility_date": "2026-04-01", "vars": {"API_KEY": "from-vars", "PLAIN": "p"}}`), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`export default { fetch(req, env) { return Response.json({ key: env.API_KEY, plain: env.PLAIN, multi: env.MULTI }); } };`), 0o644)
	os.WriteFile(filepath.Join(app, ".dev.vars"), []byte("# local only\nAPI_KEY=from-dev-vars\nMULTI=\"a\\nb\"\n"), 0o600)
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r struct {
		Smoke map[string]any `json:"smoke"`
	}
	e.MustJSON(&r, "dev", app, "--once", "--port", "0")
	if r.Smoke["key"] != "from-dev-vars" || r.Smoke["plain"] != "p" || r.Smoke["multi"] != "a\nb" {
		t.Fatalf("tw dev --once with .dev.vars: %+v", r.Smoke)
	}
}
