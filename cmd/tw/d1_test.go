package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/rosskukulinski/tailworks/internal/features"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/d1mig"
	"github.com/rosskukulinski/tailworks/mgmt"
	_ "modernc.org/sqlite"
)

// sqliteD1Host is a host push listener whose POST /v1/store-admin runs D1
// batches on a real SQLite (one transaction per batch), standing in for
// the workload's store object (hostd/d1_test.go covers the real one).
func sqliteD1Host(t *testing.T, e *pubEnv, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, id+".db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	sock := filepath.Join(e.dir, id+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/store-admin" || q.Get("kind") != "d1" || q.Get("name") != "app-db" || q.Get("workload") != "support" {
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"unknown_store","message":"no such store"}`)
			return
		}
		if q.Get("action") == "export" {
			json.NewEncoder(w).Encode(map[string]string{"sql": "PRAGMA defer_foreign_keys=TRUE;\nCREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);\n"})
			return
		}
		var req struct{ Queries []d1mig.Query }
		json.NewDecoder(r.Body).Decode(&req)
		tx, _ := db.Begin()
		var out []map[string]any
		for _, qq := range req.Queries {
			rows, err := tx.Query(qq.SQL, qq.Params...)
			if err != nil {
				tx.Rollback()
				json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
				return
			}
			cols, _ := rows.Columns()
			res := [][]any{}
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				rows.Scan(ptrs...)
				res = append(res, vals)
			}
			rows.Close()
			up := strings.ToUpper(strings.TrimSpace(qq.SQL))
			changed := strings.Contains(up, "INSERT") || strings.Contains(up, "CREATE TABLE USERS") // what writes in this test
			out = append(out, map[string]any{"success": true, "results": map[string]any{"columns": cols, "rows": res}, "meta": map[string]any{"changes": 1, "changed_db": changed}})
		}
		tx.Commit()
		json.NewEncoder(w).Encode(out)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	ctx := context.Background()
	if _, _, err := e.m.RegisterHost(ctx, mgmt.RegisterRequest{HostID: id, CapacityNodes: 10, Tiers: []string{"process"}, Features: features.Host(), PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AcquireLease(ctx, mgmt.LeaseName(e.tenantID, "support"), id, time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestCLID1: `tw d1` create/list/delete (mgmt's store routes), execute,
// export, and wrangler-layout migrations (create, list, apply) through
// mgmt to the host running the workload; the database named by binding or
// database_name, the workload taken from the app in the cwd.
func TestCLID1(t *testing.T) {
	e := newPubEnv(t)
	app := t.TempDir()
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "support", "tailnet": { "tags": ["tag:tw-wl-support"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "d1_databases": [{ "binding": "DB", "database_name": "app-db", "database_id": "6c1a…" }]}`), 0o644)
	t.Chdir(app)

	// Not running: the error says how to get there.
	if _, err := e.call(t, cmdD1, "execute", "DB", "--command", "SELECT 1"); cliCode(err) != "unknown_workload" || !strings.Contains(cliText(err), "tw deploy") {
		t.Fatalf("not running: %v", err)
	}
	sqliteD1Host(t, e, "h2")

	out, err := e.call(t, cmdD1, "migrations", "create", "DB", "add users")
	mig := filepath.Join(app, "migrations", "0001_add_users.sql")
	if err != nil || !strings.Contains(out, mig) {
		t.Fatalf("migrations create: %v\n%s", err, out)
	}
	f, _ := os.OpenFile(mig, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);\nINSERT INTO users (email) VALUES ('a@example.com');\n")
	f.Close()
	if out, err := e.call(t, cmdD1, "migrations", "list", "DB"); err != nil || !strings.Contains(out, "0001_add_users.sql") {
		t.Fatalf("migrations list: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdD1, "migrations", "apply", "app-db", "--json"); err != nil || !strings.Contains(out, `"applied": [`) || !strings.Contains(out, "0001_add_users.sql") {
		t.Fatalf("migrations apply: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdD1, "migrations", "list", "DB"); err != nil || !strings.Contains(out, "no migrations to apply") {
		t.Fatalf("migrations list after apply: %v\n%s", err, out)
	}
	out, err = e.call(t, cmdD1, "execute", "DB", "--command", "INSERT INTO users (email) VALUES ('b@example.com'); SELECT id, email FROM users ORDER BY id")
	if err != nil || !strings.Contains(out, "id  email") || !strings.Contains(out, "2   b@example.com") {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	sqlFile := filepath.Join(t.TempDir(), "q.sql")
	os.WriteFile(sqlFile, []byte("-- count\nSELECT count(*) AS n FROM users;\n"), 0o644)
	out, err = e.call(t, cmdD1, "execute", "DB", "--file", sqlFile, "--json")
	var r d1Result
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || len(r.Results) != 1 || r.Results[0].Results.Rows[0][0] != float64(2) {
		t.Fatalf("execute --file --json: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdD1, "execute", "DB", "--command", "SELECT * FROM nope"); cliCode(err) != "d1_error" || !strings.Contains(cliText(err), "no such table") {
		t.Fatalf("SQL error: %v", err)
	}
	withD1Input(t, "", true)
	if _, err := e.call(t, cmdD1, "execute", "DB"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "SQL on stdin") {
		t.Fatalf("execute without SQL on a terminal: %v", err)
	}
	if _, err := e.call(t, cmdD1, "execute", "DB", "--command", "SELECT 1", "--file", sqlFile); cliCode(err) != "bad_request" {
		t.Fatalf("execute --command --file: %v", err)
	}
	withD1Input(t, "SELECT count(*) AS n FROM users;\n", false)
	out, err = e.call(t, cmdD1, "execute", "DB", "--json")
	r = d1Result{}
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || len(r.Results) != 1 || r.Results[0].Results.Rows[0][0] != float64(2) {
		t.Fatalf("execute from stdin: %v\n%s", err, out)
	}
	withD1Input(t, "-- nothing\n", false)
	if _, err := e.call(t, cmdD1, "execute", "DB"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "SQL on stdin") {
		t.Fatalf("execute with an empty pipe: %v", err)
	}
	withD1Input(t, strings.Repeat("x", mgmt.D1BodyLimit+1), false)
	if _, err := e.call(t, cmdD1, "execute", "DB"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "over") {
		t.Fatalf("execute with too much SQL on stdin: %v", err)
	}
	dump := filepath.Join(t.TempDir(), "dump.sql")
	if out, err := e.call(t, cmdD1, "export", "DB", "--output", dump); err != nil || !strings.Contains(out, "exported app-db") {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(dump); !strings.HasPrefix(string(b), "PRAGMA defer_foreign_keys=TRUE;\nCREATE TABLE users") {
		t.Fatalf("export file: %q", b)
	}
	recs, _ := e.st.ListAudit(context.Background(), mgmt.AuditFilter{TenantID: e.tenantID, Kinds: []string{"d1_write"}})
	if len(recs) != 2 { // the migration, the INSERT
		t.Fatalf("d1_write audits: %d", len(recs))
	}

	// create / list / delete: the generic store records.
	if out, err := e.call(t, cmdD1, "create", "app-db"); err != nil || !strings.Contains(out, "database app-db of workload support") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdD1, "list"); err != nil || !strings.Contains(out, "app-db") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdD1, "delete", "app-db"); cliCode(err) != "confirmation_required" || !strings.Contains(cliText(err), "--yes") {
		t.Fatalf("delete without --yes: %v", err)
	}
	if out, err := e.call(t, cmdD1, "delete", "DB", "--yes"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if out, _ := e.call(t, cmdD1, "list"); !strings.Contains(out, "no D1 databases") {
		t.Fatalf("list after delete: %s", out)
	}
	if _, err := e.call(t, cmdD1, "migrations", "list", "OTHER"); cliCode(err) != "bad_request" {
		t.Fatalf("migrations of an unbound database: %v", err)
	}
}

// withD1Input makes tw d1 execute's stdin s, a terminal when tty.
func withD1Input(t *testing.T, s string, tty bool) {
	old := d1Input
	d1Input = func() (io.Reader, bool) { return strings.NewReader(s), tty }
	t.Cleanup(func() { d1Input = old })
}
