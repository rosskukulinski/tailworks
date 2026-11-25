package bundle_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestParseD1: wrangler's d1_databases become "d1" stores named by
// database_name (database_id is wrangler's and ignored), with wrangler's
// migration defaults; bad entries are refused.
func TestParseD1(t *testing.T) {
	wr := `{ "name": "w", "main": "index.js",
	  // as wrangler writes it
	  "d1_databases": [
	    { "binding": "DB", "database_name": "prod-db", "database_id": "4a1c7c1e-0000-4000-8000-000000000000" },
	    { "binding": "LOGS", "database_name": "logs", "migrations_dir": "db/logs", "migrations_table": "schema_log", "preview_database_id": "x" },
	  ] }`
	a, err := bundle.Parse(map[string][]byte{"index.js": []byte("export default {}"), "wrangler.jsonc": []byte(wr)})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Stores) != 2 || a.Stores[0] != (bundle.StoreBinding{Kind: "d1", Binding: "DB", Name: "prod-db"}) || a.Stores[1].Name != "logs" {
		t.Fatalf("stores: %+v", a.Stores)
	}
	dbs, err := bundle.D1Databases([]byte(wr))
	if err != nil || len(dbs) != 2 {
		t.Fatalf("D1Databases: %+v %v", dbs, err)
	}
	if dbs[0].MigrationsDir != "migrations" || dbs[0].MigrationsTable != "d1_migrations" || dbs[1].MigrationsDir != "db/logs" || dbs[1].MigrationsTable != "schema_log" {
		t.Fatalf("migration settings: %+v", dbs)
	}
	if dbs, err := bundle.D1Databases([]byte(`{"name": "w"}`)); err != nil || dbs != nil {
		t.Fatalf("no d1_databases: %v %v", dbs, err)
	}
	for entry, want := range map[string]string{
		`{ "database_name": "x" }`:                                                "no binding",
		`{ "binding": "DB" }`:                                                     "no database_name",
		`{ "binding": "DB", "database_name": "a/b" }`:                             "store name",
		`{ "binding": "DB", "database_name": "x", "migrations_dir": "../up" }`:    "inside the app directory",
		`{ "binding": "DB", "database_name": "x", "migrations_dir": "/etc" }`:     "inside the app directory",
		`{ "binding": "DB", "database_name": "x", "migrations_table": "a b" }`:    "bad migrations_table",
		`{ "binding": "DB", "database_name": "x", "migrations_table": "_cf_KV" }`: "bad migrations_table",
	} {
		_, err := bundle.Parse(map[string][]byte{"index.js": []byte("export default {}"), "wrangler.jsonc": []byte(`{ "name": "w", "main": "index.js", "d1_databases": [` + entry + `] }`)})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", entry, err, want)
		}
	}
}
