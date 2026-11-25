package d1mig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSplit(t *testing.T) {
	in := `-- Migration number: 0001
CREATE TABLE a (x TEXT DEFAULT 'semi;colon', "we;ird" INT, [b;r] INT, ` + "`c;d`" + ` INT); -- trailing ; comment
/* block ; comment */ INSERT INTO a (x) VALUES ('it''s; ok');
CREATE TRIGGER IF NOT EXISTS t AFTER INSERT ON a BEGIN
  UPDATE a SET x = CASE WHEN x = 'a' THEN 'b'; ELSE x END;
  INSERT INTO a (x) VALUES ('end;');
END;
CREATE TEMP TRIGGER t2 BEGIN SELECT 1; END
;
-- only a comment;
SELECT 1`
	got := Split(in)
	want := []string{
		"-- Migration number: 0001\nCREATE TABLE a (x TEXT DEFAULT 'semi;colon', \"we;ird\" INT, [b;r] INT, `c;d` INT)",
		"-- trailing ; comment\n/* block ; comment */ INSERT INTO a (x) VALUES ('it''s; ok')",
		"CREATE TRIGGER IF NOT EXISTS t AFTER INSERT ON a BEGIN\n  UPDATE a SET x = CASE WHEN x = 'a' THEN 'b'; ELSE x END;\n  INSERT INTO a (x) VALUES ('end;');\nEND",
		"CREATE TEMP TRIGGER t2 BEGIN SELECT 1; END",
		"-- only a comment;\nSELECT 1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split:\n%q\nwant\n%q", got, want)
	}
	if s := Split("  -- nothing\n/* here */ ;; "); len(s) != 0 {
		t.Fatalf("comment-only: %q", s)
	}
}

// fakeDB records batches; it fails a batch containing "FAIL".
type fakeDB struct {
	applied []string
	batches [][]Query
}

func (f *fakeDB) exec(_ context.Context, qs []Query) ([]Result, error) {
	f.batches = append(f.batches, qs)
	for _, q := range qs {
		if strings.Contains(q.SQL, "FAIL") {
			return nil, errors.New("D1_ERROR: near FAIL")
		}
	}
	out := make([]Result, len(qs))
	for i, q := range qs {
		out[i].Success = true
		switch {
		case strings.HasPrefix(q.SQL, "SELECT name FROM d1_migrations"):
			for _, n := range f.applied {
				out[i].Results.Rows = append(out[i].Results.Rows, []any{n})
			}
		case strings.HasPrefix(q.SQL, "INSERT INTO d1_migrations"):
			f.applied = append(f.applied, q.Params[0].(string))
		}
	}
	return out, nil
}

func TestMigrations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	now := time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)
	p1, err := Create(dir, "create users", now)
	if err != nil || filepath.Base(p1) != "0001_create_users.sql" {
		t.Fatalf("create: %s %v", p1, err)
	}
	b, _ := os.ReadFile(p1)
	if string(b) != "-- Migration number: 0001 \t 2026-11-15T01:02:03.000Z\n" {
		t.Fatalf("header: %q", b)
	}
	os.WriteFile(p1, append(b, "CREATE TABLE users (id INTEGER PRIMARY KEY);\n"...), 0o644)
	os.WriteFile(filepath.Join(dir, "0007_later.sql"), []byte("CREATE TABLE later (x);"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a migration"), 0o644)
	if p, _ := Create(dir, "next", now); filepath.Base(p) != "0008_next.sql" {
		t.Fatalf("next number: %s", p)
	}
	if _, err := Create(dir, "../escape", now); err == nil {
		t.Fatal("a message with a path separator was accepted")
	}

	db := &fakeDB{}
	ctx := context.Background()
	pend, err := Pending(ctx, db.exec, dir, "d1_migrations")
	if err != nil || len(pend) != 3 {
		t.Fatalf("pending: %v %v", pend, err)
	}
	if !strings.Contains(db.batches[0][0].SQL, "CREATE TABLE IF NOT EXISTS d1_migrations(") {
		t.Fatalf("table: %q", db.batches[0][0].SQL)
	}
	done, err := Apply(ctx, db.exec, dir, "d1_migrations")
	if err != nil || !reflect.DeepEqual(done, []string{"0001_create_users.sql", "0007_later.sql", "0008_next.sql"}) {
		t.Fatalf("apply: %v %v", done, err)
	}
	// One batch per migration: its statements, then its record.
	last := db.batches[len(db.batches)-3]
	if len(last) != 2 || !strings.Contains(last[0].SQL, "CREATE TABLE users") || last[1].Params[0] != "0001_create_users.sql" {
		t.Fatalf("batch: %+v", last)
	}
	if done, err := Apply(ctx, db.exec, dir, "d1_migrations"); err != nil || len(done) != 0 {
		t.Fatalf("second apply: %v %v", done, err)
	}

	// A failing migration stops the run, unrecorded.
	os.WriteFile(filepath.Join(dir, "0009_bad.sql"), []byte("CREATE TABLE ok (x); FAIL;"), 0o644)
	os.WriteFile(filepath.Join(dir, "0010_after.sql"), []byte("CREATE TABLE after (x);"), 0o644)
	done, err = Apply(ctx, db.exec, dir, "d1_migrations")
	if err == nil || len(done) != 0 || !strings.Contains(err.Error(), "0009_bad.sql") {
		t.Fatalf("bad migration: %v %v", done, err)
	}
	if len(db.applied) != 3 {
		t.Fatalf("recorded: %v", db.applied)
	}
	if _, err := Applied(ctx, db.exec, "x; DROP TABLE y"); err == nil {
		t.Fatal("bad table name accepted")
	}
}
