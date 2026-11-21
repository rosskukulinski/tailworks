package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// d1Client drives the d1app fixture: POST a JSON body to a route of the
// worker (or, with the /do prefix, of the facet object Box), get JSON.
type d1Client struct {
	t    *testing.T
	s    *suite
	base string
}

func (c d1Client) post(path string, body any) map[string]any {
	c.t.Helper()
	b, _ := json.Marshal(body)
	code, _, out := c.s.do("POST", c.base+path, b, "content-type", "application/json")
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || code != 200 {
		c.t.Fatalf("POST %s %s: %d %s", path, b, code, out)
	}
	return m
}

func (c d1Client) with(t *testing.T) d1Client {
	c.t = t
	return c
}

// col runs first(col).
func (c d1Client) col(sql, col string, params ...any) any {
	c.t.Helper()
	m := c.post("/q", map[string]any{"sql": sql, "method": "first", "col": col, "params": params})
	if e, ok := m["error"]; ok {
		c.t.Fatalf("first(%s) %s %v: %v", col, sql, params, e)
	}
	return m["r"]
}

// q runs one prepared statement; it fails the test on a D1 error.
func (c d1Client) q(sql, method string, params ...any) any {
	c.t.Helper()
	m := c.post("/q", map[string]any{"sql": sql, "method": method, "params": params})
	if e, ok := m["error"]; ok {
		c.t.Fatalf("%s %s %v: %v", method, sql, params, e)
	}
	return m["r"]
}

// qErr runs one statement that must fail and returns the error.
func (c d1Client) qErr(body map[string]any) string {
	c.t.Helper()
	m := c.post("/q", body)
	e, _ := m["error"].(string)
	if e == "" {
		c.t.Fatalf("%v: no error: %v", body, m)
	}
	return e
}

// TestD1 is the D1 binding (ADR-038) on real workerd: workerd's own D1 API
// in the worker, the same API in a facet, the prepared-statement API, type
// mapping, batch atomicity, exec, limits, sessions, migrations from a
// wrangler-layout directory, operator queries and export (hostd's store
// admin op), journal-first writes (one append per write, none per read),
// and recovery of the whole database from mgmt's journal after the host
// was fenced and lost its DO storage.
func TestD1(t *testing.T) {
	const fenceAfter = time.Second
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.FenceAfter, o.HeartbeatEvery = fenceAfter, 100*time.Millisecond
	})
	e := s.e
	fixture := hostdtest.Fixture("d1app")
	dep := e.Deploy(fixture, "d1app", nil)
	wl := e.WaitActive("d1app", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	W := d1Client{t, s, "http://" + wl.IP}
	F := d1Client{t, s, "http://" + wl.IP + "/do"}
	ctx := context.Background()
	admin := func(action string, arg any) []byte {
		t.Helper()
		b, _ := json.Marshal(arg)
		out, err := e.Host.StoreAdmin(ctx, e.TenantID, "d1app", "d1", "app-db", action, b)
		if err != nil {
			t.Fatalf("store admin %s: %v", action, err)
		}
		return out
	}
	exec := func(ctx context.Context, qs []d1mig.Query) ([]d1mig.Result, error) {
		out := admin("query", map[string]any{"queries": qs})
		var rs []d1mig.Result
		if err := json.Unmarshal(out, &rs); err != nil {
			var one d1mig.Result
			if json.Unmarshal(out, &one) == nil && !one.Success {
				return nil, fmt.Errorf("D1_ERROR: %s", one.Error)
			}
			return nil, err
		}
		return rs, nil
	}

	t.Run("Binding", func(t *testing.T) {
		w, f := W.with(t), F.with(t)
		want := `{"methods":["prepare","batch","exec","withSession","dump"],"type":"object"}`
		for _, c := range []d1Client{w, f} {
			if got := js(c.post("/info", nil)); got != want {
				t.Fatalf("%s: env.DB %s, want %s", c.base, got, want)
			}
		}
	})

	t.Run("Migrations", func(t *testing.T) {
		w := W.with(t)
		dir := filepath.Join(fixture, "migrations")
		done, err := d1mig.Apply(ctx, exec, dir, "d1_migrations")
		if err != nil || js(done) != `["0001_create_users.sql","0002_posts.sql"]` {
			t.Fatalf("apply: %v %v", done, err)
		}
		if done, err := d1mig.Apply(ctx, exec, dir, "d1_migrations"); err != nil || len(done) != 0 {
			t.Fatalf("second apply: %v %v", done, err)
		}
		if got := js(w.q("SELECT name FROM d1_migrations ORDER BY id", "raw")); got != `[["0001_create_users.sql"],["0002_posts.sql"]]` {
			t.Fatalf("migrations table: %s", got)
		}
		if got := js(w.q("SELECT email FROM users", "raw")); got != `[["first@example.com"]]` {
			t.Fatalf("users: %s", got)
		}
		w.q("INSERT INTO posts (user_id) VALUES (1)", "run") // the trigger fires
		if got := js(w.col("SELECT body FROM posts", "body")); got != `"it's; fine"` {
			t.Fatalf("default with a quote and a semicolon: %s", got)
		}
		// A failing migration applies nothing and isn't recorded.
		bad := t.TempDir()
		os.WriteFile(filepath.Join(bad, "0003_bad.sql"), []byte("CREATE TABLE half (x);\nINSERT INTO nope VALUES (1);\n"), 0o644)
		if _, err := d1mig.Apply(ctx, exec, bad, "d1_migrations"); err == nil || !strings.Contains(err.Error(), "no such table: nope") {
			t.Fatalf("bad migration: %v", err)
		}
		if got := js(w.col("SELECT count(*) AS n FROM sqlite_master WHERE name = 'half'", "n")); got != "0" {
			t.Fatalf("half-applied migration: %s", got)
		}
	})

	t.Run("PreparedStatements", func(t *testing.T) {
		w := W.with(t)
		w.q("CREATE TABLE kv (k TEXT PRIMARY KEY, v)", "run")
		r := w.q("INSERT INTO kv (k, v) VALUES (?1, ?2)", "run", "a", 1).(map[string]any)
		meta := r["meta"].(map[string]any)
		if r["success"] != true || meta["changes"] != float64(1) || meta["last_row_id"] != float64(1) || meta["changed_db"] != true || meta["rows_written"].(float64) < 1 {
			t.Fatalf("run: %v", r)
		}
		w.q("INSERT INTO kv (k, v) VALUES (?, ?), (?, ?)", "run", "b", 2, "c", 3)
		all := w.q("SELECT k, v FROM kv WHERE v >= ? ORDER BY k", "all", 2).(map[string]any)
		if js(all["results"]) != `[{"k":"b","v":2},{"k":"c","v":3}]` || all["success"] != true {
			t.Fatalf("all: %v", all)
		}
		if am := all["meta"].(map[string]any); am["changed_db"] != false || am["rows_read"].(float64) < 2 {
			t.Fatalf("all meta: %v", am)
		}
		if got := js(w.q("SELECT k, v FROM kv ORDER BY k", "first")); got != `{"k":"a","v":1}` {
			t.Fatalf("first: %s", got)
		}
		if got := js(w.q("SELECT v FROM kv WHERE k = ?", "first", "c")); got != `{"v":3}` {
			t.Fatalf("first with bind: %s", got)
		}
		if got := js(w.q("SELECT k FROM kv WHERE k = 'zz'", "first")); got != "null" {
			t.Fatalf("first, no row: %s", got)
		}
		if got := js(w.post("/q", map[string]any{"sql": "SELECT v FROM kv WHERE k = 'b'", "method": "first", "col": "v"})["r"]); got != "2" {
			t.Fatalf("first(col): %s", got)
		}
		if e := w.qErr(map[string]any{"sql": "SELECT v FROM kv", "method": "first", "col": "nope"}); !strings.Contains(e, "D1_COLUMN_NOTFOUND") {
			t.Fatalf("first(missing col): %s", e)
		}
		// RETURNING: the applied write's rows come back to the caller.
		if got := js(w.q("INSERT INTO kv (k, v) VALUES ('ret', 5) RETURNING k, v * 2 AS dbl", "first")); got != `{"dbl":10,"k":"ret"}` {
			t.Fatalf("RETURNING: %s", got)
		}
		w.q("DELETE FROM kv WHERE k = 'ret'", "run")
		if got := js(w.q("SELECT k, v FROM kv ORDER BY k LIMIT 2", "raw")); got != `[["a",1],["b",2]]` {
			t.Fatalf("raw: %s", got)
		}
		if got := js(w.post("/q", map[string]any{"sql": "SELECT k, v AS val FROM kv ORDER BY k LIMIT 1", "method": "raw", "columnNames": true})["r"]); got != `[["k","val"],["a",1]]` {
			t.Fatalf("raw columnNames: %s", got)
		}
		if e := w.qErr(map[string]any{"sql": "SELEC 1"}); !strings.HasPrefix(e, "D1_ERROR: ") || !strings.Contains(e, "syntax error") {
			t.Fatalf("syntax error: %s", e)
		}
		if e := w.qErr(map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('a', 9)", "method": "run"}); !strings.Contains(e, "UNIQUE constraint failed: kv.k") {
			t.Fatalf("constraint: %s", e)
		}
		// Durable Objects' reserved names stay reserved; transactions are
		// batch()'s job, as on D1.
		if e := w.qErr(map[string]any{"sql": "SELECT * FROM _cf_KV"}); !strings.Contains(e, "D1_ERROR") {
			t.Fatalf("_cf_KV: %s", e)
		}
		if e := w.qErr(map[string]any{"sql": "BEGIN TRANSACTION", "method": "run"}); !strings.Contains(e, "D1_ERROR") {
			t.Fatalf("BEGIN: %s", e)
		}
		// The store's own state is not in the tenant's SQL namespace.
		if got := js(w.q("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT IN ('users', 'posts', 'kv', 'd1_migrations', 'sqlite_sequence', '_cf_KV')", "raw")); got != `[]` {
			t.Fatalf("extra tables: %s", got)
		}
		if e := w.post("/dump", nil)["error"]; e == nil || !strings.Contains(e.(string), "D1_DUMP_ERROR") {
			t.Fatalf("dump: %v", e)
		}
	})

	t.Run("Types", func(t *testing.T) {
		w, f := W.with(t), F.with(t)
		for _, c := range []d1Client{w, f} {
			c.q("CREATE TABLE IF NOT EXISTS ty (i INTEGER, r REAL, t TEXT, b BLOB, n, big INTEGER, flag INTEGER)", "run")
			c.q("DELETE FROM ty", "run")
			c.q("INSERT INTO ty VALUES (?, ?, ?, ?, ?, ?, ?)", "run", 42, 2.5, "héllo ✓", map[string]any{"$u8": []int{0, 1, 255}}, nil, 9007199254740993, true)
			got := js(c.q("SELECT * FROM ty", "first"))
			// D1's mapping: INTEGER and REAL are numbers (integers beyond
			// 2^53 lose precision), a BLOB is an array of bytes, NULL is null,
			// a boolean binds as 1/0.
			if want := `{"b":[0,1,255],"big":9007199254740992,"flag":1,"i":42,"n":null,"r":2.5,"t":"héllo ✓"}`; got != want {
				t.Fatalf("%s: row %s, want %s", c.base, got, want)
			}
			if got := js(c.q("SELECT typeof(i), typeof(r), typeof(t), typeof(b), typeof(n), typeof(big) FROM ty", "raw")); got != `[["integer","real","text","blob","null","integer"]]` {
				t.Fatalf("%s: storage classes %s", c.base, got)
			}
			// A JS number binds as a REAL (Durable Object SQLite, as under
			// wrangler dev); a column with INTEGER affinity stores it as an
			// integer, and it reads back as the same JS number either way.
			if got := js(c.q("SELECT typeof(?1) AS a, typeof(CAST(?1 AS INTEGER)) AS b, ?1 + 1 AS c", "first", 7)); got != `{"a":"real","b":"integer","c":8}` {
				t.Fatalf("%s: a bound number: %s", c.base, got)
			}
			if got := js(c.col("SELECT length(b) AS n FROM ty WHERE b = ?", "n", map[string]any{"$u8": []int{0, 1, 255}})); got != "3" {
				t.Fatalf("%s: blob param in WHERE: %s", c.base, got)
			}
			if e := c.qErr(map[string]any{"sql": "SELECT ?", "params": []any{map[string]any{"$big": "5"}}}); !strings.Contains(e, "D1_TYPE_ERROR") {
				t.Fatalf("%s: bigint: %s", c.base, e)
			}
		}
	})

	t.Run("Batch", func(t *testing.T) {
		w, f := W.with(t), F.with(t)
		count := func() string { return js(w.col("SELECT count(*) AS n FROM kv", "n")) }
		before := count()
		r := w.post("/batch", []map[string]any{
			{"sql": "INSERT INTO kv (k, v) VALUES (?, ?)", "params": []any{"x1", 1}},
			{"sql": "INSERT INTO kv (k, v) VALUES (?, ?)", "params": []any{"a", 2}}, // UNIQUE
		})
		if e, _ := r["error"].(string); !strings.Contains(e, "UNIQUE constraint failed") {
			t.Fatalf("failing batch: %v", r)
		}
		if got := count(); got != before {
			t.Fatalf("failing batch left rows: %s → %s", before, got)
		}
		r = f.post("/batch", []map[string]any{ // from the facet
			{"sql": "INSERT INTO kv (k, v) VALUES (?, ?)", "params": []any{"x1", 1}},
			{"sql": "UPDATE kv SET v = v + 100 WHERE k = ?", "params": []any{"x1"}},
			{"sql": "SELECT v FROM kv WHERE k = ?", "params": []any{"x1"}},
		})
		rs, _ := r["r"].([]any)
		if len(rs) != 3 || js(rs[2].(map[string]any)["results"]) != `[{"v":101}]` || rs[1].(map[string]any)["meta"].(map[string]any)["changes"] != float64(1) {
			t.Fatalf("batch: %v", r)
		}
		r = w.post("/exec", map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('e1', 1);\nINSERT INTO kv (k, v) VALUES ('e2', 2);\nUPDATE kv SET v = 3 WHERE k = 'e2';"})
		if er := r["r"].(map[string]any); er["count"] != float64(3) {
			t.Fatalf("exec: %v", r)
		}
		r = w.post("/exec", map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('e3', 1);\nINSERT INTO nope VALUES (1);"})
		if e, _ := r["error"].(string); !strings.Contains(e, "D1_EXEC_ERROR: Error in line 2: INSERT INTO nope VALUES (1);: no such table: nope") {
			t.Fatalf("exec error: %v", r)
		}
		if got := js(w.col("SELECT count(*) AS n FROM kv WHERE k = 'e3'", "n")); got != "0" {
			t.Fatalf("a failing exec() applied a line: %s", got)
		}
	})

	t.Run("JournalFirst", func(t *testing.T) {
		w, f := W.with(t), F.with(t)
		n0 := e.FM.StoreAppends()
		w.q("SELECT * FROM kv ORDER BY v DESC", "all")                      // a read with a sort
		w.q("WITH x AS (SELECT k FROM kv) SELECT count(*) FROM x", "all")   // a CTE read
		w.q("UPDATE kv SET v = 0 WHERE k = 'no such key'", "run")           // a write that writes nothing
		w.q("CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v)", "run") // DDL that changes nothing
		if n := e.FM.StoreAppends(); n != n0 {
			t.Fatalf("reads and no-op writes appended %d journal entries", n-n0)
		}
		w.q("UPDATE kv SET v = v WHERE k = 'a'", "run")
		f.post("/batch", []map[string]any{{"sql": "DELETE FROM kv WHERE k = 'e1'"}, {"sql": "DELETE FROM kv WHERE k = 'e2'"}})
		if n := e.FM.StoreAppends(); n != n0+2 {
			t.Fatalf("2 writes appended %d entries, want 2 (one per request)", n-n0)
		}
		// A CTE that writes is caught by the read path and journaled.
		w.q("WITH n AS (SELECT 7 AS v) INSERT INTO kv (k, v) SELECT 'cte', v FROM n", "run")
		if n := e.FM.StoreAppends(); n != n0+3 {
			t.Fatalf("a writing CTE appended %d entries, want 1", n-n0-2)
		}
	})

	t.Run("Limits", func(t *testing.T) {
		w := W.with(t)
		ps := make([]any, 101)
		ph := make([]string, 101)
		for i := range ps {
			ps[i], ph[i] = i, "?"
		}
		if e := w.qErr(map[string]any{"sql": "SELECT " + strings.Join(ph, ","), "params": ps}); !strings.Contains(e, "too many SQL variables") {
			t.Fatalf("101 params: %s", e)
		}
		if e := w.qErr(map[string]any{"sql": "SELECT '" + strings.Repeat("x", 100_001) + "'"}); !strings.Contains(e, "statement too long") {
			t.Fatalf("long statement: %s", e)
		}
		if e := w.qErr(map[string]any{"sql": "SELECT ?", "params": []any{strings.Repeat("y", 2_000_001)}}); !strings.Contains(e, "over 2000000 bytes") {
			t.Fatalf("big value: %s", e)
		}
		big := "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 200000) SELECT x, printf('%0100d', x) AS pad FROM c"
		if e := w.qErr(map[string]any{"sql": big}); !strings.Contains(e, "result too large") {
			t.Fatalf("big result: %s", e)
		}
		n0 := e.FM.StoreAppends()
		if e := w.qErr(map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('big', ?)", "method": "run", "params": []any{map[string]any{"$u8": make([]int, 1_900_000)}}}); !strings.Contains(e, "write too large") {
			t.Fatalf("write over the journal's entry size: %s", e)
		}
		if e.FM.StoreAppends() != n0 {
			t.Fatal("a refused write reached the journal")
		}
	})

	t.Run("Sessions", func(t *testing.T) {
		w, f := W.with(t), F.with(t)
		r := w.post("/session", map[string]any{"constraint": "first-primary", "steps": []any{
			map[string]any{"sql": "SELECT count(*) AS n FROM kv", "method": "first", "col": "n"},
			map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('s1', 1)", "method": "run"},
		}})
		steps, _ := r["steps"].([]any)
		if len(steps) != 2 {
			t.Fatalf("session: %v", r)
		}
		b1, _ := steps[0].(map[string]any)["bookmark"].(string)
		b2, _ := steps[1].(map[string]any)["bookmark"].(string)
		if b1 == "" || b2 == "" || !(b1 < b2) {
			t.Fatalf("bookmarks %q then %q: want increasing", b1, b2)
		}
		// A new session from that bookmark (e.g. on another replica) reads
		// the write; from the facet too.
		for _, c := range []d1Client{w, f} {
			r = c.post("/session", map[string]any{"constraint": b2, "steps": []any{map[string]any{"sql": "SELECT v FROM kv WHERE k = 's1'", "method": "first", "col": "v"}}})
			if st := r["steps"].([]any)[0].(map[string]any); st["r"] != float64(1) || st["bookmark"] != b2 {
				t.Fatalf("%s: session from a bookmark: %v", c.base, r)
			}
		}
		r = w.post("/session", map[string]any{"steps": []any{map[string]any{"sql": "SELECT 1 AS one", "method": "first", "col": "one"}}})
		if st := r["steps"].([]any)[0].(map[string]any); st["r"] != float64(1) || st["bookmark"] == nil {
			t.Fatalf("unconstrained session: %v", r)
		}
	})

	var exported string
	t.Run("AdminQueryAndExport", func(t *testing.T) {
		w := W.with(t)
		var rs []d1mig.Result
		json.Unmarshal(admin("query", map[string]any{"queries": []d1mig.Query{{SQL: "INSERT INTO kv (k, v) VALUES (?, ?)", Params: []any{"op", "from the operator"}}, {SQL: "SELECT v FROM kv WHERE k = 'op'"}}}), &rs)
		if len(rs) != 2 || !rs[1].Success || js(rs[1].Results.Rows) != `[["from the operator"]]` {
			t.Fatalf("admin query: %+v", rs)
		}
		if got := js(w.col("SELECT v FROM kv WHERE k = 'op'", "v")); got != `"from the operator"` {
			t.Fatalf("the worker doesn't see the operator's write: %s", got)
		}
		var one d1mig.Result
		json.Unmarshal(admin("query", map[string]any{"queries": []d1mig.Query{{SQL: "SELECT * FROM nope"}}}), &one)
		if one.Success || !strings.Contains(one.Error, "no such table") {
			t.Fatalf("admin query error: %+v", one)
		}
		var ex struct{ SQL string }
		json.Unmarshal(admin("export", map[string]any{}), &ex)
		exported = ex.SQL
		for _, want := range []string{
			"PRAGMA defer_foreign_keys=TRUE;",
			"CREATE TABLE users (",
			`INSERT INTO "users" VALUES(1,'first@example.com',`,
			`INSERT INTO "ty" VALUES(42,2.5,'héllo ✓',X'0001FF',NULL,9007199254740992,1);`,
			"CREATE INDEX posts_user ON posts(user_id);",
			"CREATE TRIGGER posts_touch AFTER INSERT ON posts BEGIN",
		} {
			if !strings.Contains(exported, want) {
				t.Fatalf("export lacks %q:\n%s", want, exported)
			}
		}
		if strings.Contains(exported, "_cf_") || strings.Contains(exported, "__tw") {
			t.Fatalf("export has platform state:\n%s", exported)
		}
		if _, err := e.Host.StoreAdmin(ctx, e.TenantID, "d1app", "d1", "other-db", "query", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "binds no d1 store") {
			t.Fatalf("admin op on an undeclared store: %v", err)
		}
	})

	// The host fences the workload (no heartbeat) and its DO storage is
	// lost (as on a new host after a lease move): the database comes back
	// from mgmt's journal alone, byte for byte as exported.
	t.Run("RecoveredFromJournal", func(t *testing.T) {
		ws := workloadStatus(t, e, "d1app")
		if ws.DOStorage == "" {
			t.Fatal("no DO storage dir in status")
		}
		e.FM.SetHeartbeatsDown(true)
		deadline := time.Now().Add(10 * time.Second)
		for workloadStatus(t, e, "d1app").PID != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("workload not fenced: %+v", workloadStatus(t, e, "d1app"))
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err := os.RemoveAll(ws.DOStorage); err != nil {
			t.Fatal(err)
		}
		e.FM.SetHeartbeatsDown(false)
		wl = e.WaitActive("d1app", dep, 60*time.Second)
		s.waitPeer(wl.IP)
		W.base, F.base = "http://"+wl.IP, "http://"+wl.IP+"/do"
		w, f := W.with(t), F.with(t)
		if got := js(w.col("SELECT v FROM kv WHERE k = 'op'", "v")); got != `"from the operator"` {
			t.Fatalf("after losing DO storage: %s", got)
		}
		var ex struct{ SQL string }
		json.Unmarshal(admin("export", map[string]any{}), &ex)
		// DEFAULT CURRENT_TIMESTAMP (users.created_at, d1_migrations.
		// applied_at) is evaluated again on replay (ADR-038: statement
		// replay); compare everything else.
		ts := regexp.MustCompile(`'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d'`)
		norm := func(s string) string { return ts.ReplaceAllString(s, "'<now>'") }
		if norm(ex.SQL) != norm(exported) {
			t.Fatalf("recovered database differs:\n%s\nwant\n%s", ex.SQL, exported)
		}
		if got := js(f.col("SELECT count(*) AS n FROM d1_migrations", "n")); got != "2" {
			t.Fatalf("migrations after recovery: %s", got)
		}
	})

	// A journal entry that fails on replay (#106) is not skipped: replay
	// stops before it, the bookmark (the local seq) stays at the entry
	// before, reads serve what applied, writes fail with store_degraded,
	// and an export says what's missing.
	t.Run("ReplayFailureStops", func(t *testing.T) {
		w := W.with(t)
		bad := degradeStore(t, e, "/v1/tenants/"+e.TenantID+"/workloads/d1app/stores/d1/app-db/journal",
			[]byte(`{"v":1,"id":"bad","q":[{"s":"INSERT INTO no_such_table VALUES (1)"}]}`),
			[]byte(`{"v":1,"id":"good","q":[{"s":"INSERT INTO kv (k, v) VALUES ('after-bad', 1)"}]}`))
		e1 := w.qErr(map[string]any{"sql": "INSERT INTO kv (k, v) VALUES ('w-degraded', 1)", "method": "run"})
		if !strings.Contains(e1, "[store_degraded]") || !strings.Contains(e1, "no such table: no_such_table") || !strings.Contains(e1, fmt.Sprintf("entry %d failed", bad)) {
			t.Fatalf("write to a degraded database: %s", e1)
		}
		if got := js(w.col("SELECT v FROM kv WHERE k = 'op'", "v")); got != `"from the operator"` {
			t.Fatalf("read from a degraded database: %s", got)
		}
		if got := js(w.col("SELECT count(*) AS n FROM kv WHERE k IN ('after-bad', 'w-degraded')", "n")); got != "0" {
			t.Fatalf("applied past the failing entry: %s rows", got)
		}
		r := w.post("/session", map[string]any{"steps": []any{map[string]any{"sql": "SELECT 1 AS one", "method": "first", "col": "one"}}})
		bm, _ := r["steps"].([]any)[0].(map[string]any)["bookmark"].(string)
		if want := fmt.Sprintf("%016d.", bad-1); !strings.HasPrefix(bm, want) {
			t.Fatalf("bookmark %q of a degraded database: want %s… (synced only up to the entry before the failing one)", bm, want)
		}
		if _, err := e.Host.StoreAdmin(ctx, e.TenantID, "d1app", "d1", "app-db", "query", []byte(`{"queries":[{"sql":"INSERT INTO kv (k, v) VALUES ('op2', 1)"}]}`)); err == nil || !strings.Contains(err.Error(), "store_degraded") {
			t.Fatalf("operator write to a degraded database: %v", err)
		}
		var ex struct{ SQL string }
		json.Unmarshal(admin("export", map[string]any{}), &ex)
		if !strings.HasPrefix(ex.SQL, fmt.Sprintf("-- tailworks: degraded export: journal entry %d failed to apply", bad)) || !strings.Contains(ex.SQL, `'from the operator'`) || strings.Contains(ex.SQL, "after-bad") {
			t.Fatalf("export of a degraded database:\n%s", ex.SQL)
		}
	})

	// An admin deletes the database: the object empties itself on its next
	// sync; the binding then works on a new, empty database (which also
	// recovers it from ReplayFailureStops' degraded state).
	t.Run("Delete", func(t *testing.T) {
		w := W.with(t)
		if err := e.Mgmt.Do(ctx, "DELETE", "/v1/tenants/"+e.TenantID+"/workloads/d1app/stores/d1/app-db", nil, nil); err != nil {
			t.Fatal(err)
		}
		// _cf_KV is workerd's (as on D1, it is listed but not readable), and
		// SQLite's sqlite_sequence can only be emptied.
		r := w.post("/session", map[string]any{"constraint": "first-primary", "steps": []any{map[string]any{"sql": "SELECT name FROM sqlite_master WHERE name NOT LIKE '\\_cf\\_%' ESCAPE '\\' AND name != 'sqlite_sequence'", "method": "raw"}}})
		if got := js(r["steps"].([]any)[0].(map[string]any)["r"]); got != "[]" {
			t.Fatalf("after delete: %s", got)
		}
		w.q("CREATE TABLE fresh (x)", "run")
		if !reflect.DeepEqual(w.q("SELECT name FROM sqlite_master WHERE name NOT LIKE '\\_cf\\_%' ESCAPE '\\' AND name != 'sqlite_sequence'", "raw"), []any{[]any{"fresh"}}) {
			t.Fatal("new database")
		}
	})
}
