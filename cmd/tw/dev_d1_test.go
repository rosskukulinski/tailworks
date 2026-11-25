package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevD1: `tw dev` runs a D1 binding locally and applies the
// database's migrations (wrangler's migrations_dir layout) before the
// first request, like `wrangler d1 migrations apply --local`.
func TestDevD1(t *testing.T) {
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
	app := filepath.Join(work, "dd")
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.MkdirAll(filepath.Join(app, "db"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "dd", "tailnet": { "tags": ["tag:tw-wl-dd"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "dd", "main": "src/index.js", "compatibility_date": "2026-04-01",
  // wrangler's keys, a custom migrations dir and table
  "d1_databases": [{ "binding": "DB", "database_name": "dd-db", "database_id": "x", "migrations_dir": "db", "migrations_table": "schema_log" }]}`), 0o644)
	os.WriteFile(filepath.Join(app, "db", "0001_init.sql"), []byte("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);\nINSERT INTO notes (body) VALUES ('hello from a migration');\n"), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`export default { async fetch(req, env) {
  await env.DB.prepare("INSERT INTO notes (body) VALUES (?)").bind("from the worker").run();
  const { results } = await env.DB.prepare("SELECT body FROM notes ORDER BY id").all();
  const log = await env.DB.prepare("SELECT name FROM schema_log").raw();
  return Response.json({ notes: results.map((r) => r.body), log });
} };`), 0o644)
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r struct {
		Smoke map[string]any `json:"smoke"`
		D1    []struct {
			Binding, Database string
			Applied           []string
		} `json:"d1"`
	}
	e.MustJSON(&r, "dev", app, "--once", "--port", "0")
	got, _ := json.Marshal(map[string]any{"log": r.Smoke["log"], "notes": r.Smoke["notes"]})
	if string(got) != `{"log":[["0001_init.sql"]],"notes":["hello from a migration","from the worker"]}` {
		t.Fatalf("tw dev --once with D1: %+v", r.Smoke)
	}
	if len(r.D1) != 1 || r.D1[0].Binding != "DB" || r.D1[0].Database != "dd-db" || len(r.D1[0].Applied) != 1 {
		t.Fatalf("dev d1: %+v", r.D1)
	}
}
