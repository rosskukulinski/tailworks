//go:build e2evm && (linux || darwin)

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
)

// TestE2EVMD1: the D1 binding (ADR-038) on the microVM tier, with the
// process tier's fixture (hostd/testdata/apps/d1app). The database's Store
// object runs in the guest's workerd on the encrypted DO volume and
// journals each write to mgmt through hostd's store API. Covers the
// binding in the Worker and in a facet, migrations applied by the operator
// path (hostd's store admin op), prepared statements, a batch, and the
// rows surviving a VM kill and a redeploy.
func TestE2EVMD1(t *testing.T) {
	s := startVMSuite(t, nil)
	fixture := hostdtest.Fixture("d1app")
	base := s.deploy(fixture, "d1app", nil)
	W, F := base, base+"/do"
	ctx := context.Background()
	q := func(prefix, sql, method string, params ...any) any {
		t.Helper()
		if params == nil {
			params = []any{}
		}
		return s.must("POST", prefix+"/q", map[string]any{"sql": sql, "method": method, "params": params})["r"]
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	for _, p := range []string{W, F} {
		if got := js(s.must("POST", p+"/info", struct{}{})); got != `{"methods":["prepare","batch","exec","withSession","dump"],"type":"object"}` {
			t.Fatalf("%s: env.DB %s", p, got)
		}
	}

	// Migrations through hostd's store admin op (what `tw d1 migrations
	// apply` uses), into the database in the guest.
	exec := func(ctx context.Context, qs []d1mig.Query) ([]d1mig.Result, error) {
		b, _ := json.Marshal(map[string]any{"queries": qs})
		out, err := s.e.Host.StoreAdmin(ctx, s.e.TenantID, "d1app", "d1", "app-db", "query", b)
		if err != nil {
			return nil, err
		}
		var rs []d1mig.Result
		if err := json.Unmarshal(out, &rs); err != nil {
			return nil, fmt.Errorf("%v: %s", err, out)
		}
		return rs, nil
	}
	done, err := d1mig.Apply(ctx, exec, filepath.Join(fixture, "migrations"), "d1_migrations")
	if err != nil || js(done) != `["0001_create_users.sql","0002_posts.sql"]` {
		t.Fatalf("migrations: %v %v", done, err)
	}
	if got := js(q(W, "SELECT email FROM users", "raw")); got != `[["first@example.com"]]` {
		t.Fatalf("users after migrations: %s", got)
	}

	// Writes from the Worker and from the facet, a batch, reads from both.
	q(W, "CREATE TABLE kv (k TEXT PRIMARY KEY, v)", "run")
	r := q(W, "INSERT INTO kv (k, v) VALUES (?1, ?2)", "run", "a", 1).(map[string]any)
	if meta, _ := r["meta"].(map[string]any); r["success"] != true || meta["changes"] != float64(1) {
		t.Fatalf("run: %v", r)
	}
	q(F, "INSERT INTO kv (k, v) VALUES (?, ?)", "run", "f", "from-facet")
	s.must("POST", W+"/batch", []map[string]any{
		{"sql": "INSERT INTO kv (k, v) VALUES (?, ?)", "params": []any{"b", 2}},
		{"sql": "INSERT INTO kv (k, v) VALUES (?, ?)", "params": []any{"c", 3}},
	})
	const want = `[["a",1],["b",2],["c",3],["f","from-facet"]]`
	check := func(when string) {
		t.Helper()
		for _, p := range []string{W, F} {
			if got := js(q(p, "SELECT k, v FROM kv ORDER BY k", "raw")); got != want {
				t.Fatalf("%s: %s rows %s, want %s", when, p, got, want)
			}
		}
		if got := js(q(W, "SELECT name FROM d1_migrations ORDER BY id", "raw")); got != `[["0001_create_users.sql"],["0002_posts.sql"]]` {
			t.Fatalf("%s: migrations table %s", when, got)
		}
	}
	check("in the microVM")

	// A guest crash: the acknowledged writes are on the volume (and in the
	// journal); a new VM serves them.
	q(W, "INSERT INTO kv (k, v) VALUES ('lastack', 9)", "run")
	s.killVM("d1app", W+"/info")
	if got := js(q(W, "SELECT v FROM kv WHERE k = 'lastack'", "first")); got != `{"v":9}` {
		t.Fatalf("the write acknowledged just before the VM kill: %s", got)
	}
	q(W, "DELETE FROM kv WHERE k = 'lastack'", "run")
	check("after the VM kill")

	s.redeploy(fixture, "d1app")
	check("after a redeploy")
	if done, err := d1mig.Apply(ctx, exec, filepath.Join(fixture, "migrations"), "d1_migrations"); err != nil || len(done) != 0 {
		t.Fatalf("migrations re-applied after the redeploy: %v %v", done, err)
	}
}
