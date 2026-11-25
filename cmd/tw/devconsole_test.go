package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
	"github.com/rosskukulinski/tailworks/internal/kvwire"
)

// fakeD1 answers the console's queries over one table, notes(id, body).
func fakeD1(t *testing.T, seen *[]string) func(context.Context, string, []d1mig.Query) ([]d1mig.Result, error) {
	return func(_ context.Context, db string, qs []d1mig.Query) ([]d1mig.Result, error) {
		if db != "app-db" {
			return nil, errors.New("unknown db " + db)
		}
		var out []d1mig.Result
		for _, q := range qs {
			*seen = append(*seen, q.SQL)
			var r d1mig.Result
			r.Success = true
			switch {
			case q.SQL == d1TablesQuery:
				r.Results.Columns = []string{"name"}
				r.Results.Rows = [][]any{{"notes"}}
			case q.SQL == `SELECT COUNT(*) FROM "notes"`:
				r.Results.Columns = []string{"COUNT(*)"}
				r.Results.Rows = [][]any{{float64(2)}}
			case strings.HasPrefix(q.SQL, `SELECT * FROM "notes" LIMIT 51 OFFSET 0`):
				r.Results.Columns = []string{"id", "body"}
				r.Results.Rows = [][]any{{float64(1), "<b>hello</b>"}, {float64(2), nil}}
			default:
				t.Errorf("unexpected SQL %q", q.SQL)
			}
			out = append(out, r)
		}
		return out, nil
	}
}

func devConsoleForTest(t *testing.T, seen *[]string) (*devConsole, http.Handler) {
	now := time.Unix(1_800_000_000, 0)
	put := func(seq int64, k, v string, x int64) fakemgmt.StoreSnapshotEntry {
		return fakemgmt.StoreSnapshotEntry{Seq: seq, CKey: k, Data: kvwire.Encode(kvwire.Header{Key: k, Expiration: x}, []byte(v))}
	}
	del := fakemgmt.StoreSnapshotEntry{Seq: 4, CKey: "gone", Data: kvwire.Encode(kvwire.Header{Key: "gone", Delete: true}, nil)}
	snaps := []fakemgmt.StoreSnapshot{
		{Workload: "app", Kind: "kv", Name: "cache", Entries: []fakemgmt.StoreSnapshotEntry{
			put(1, "user:1", `{"name":"Ada"}`, 0),
			put(2, "gone", "x", 0),
			put(3, "expired", "x", now.Unix()-1),
			del,
			put(5, "bin", "\xff\xfe", 0),
			put(6, "user:2", "<script>alert(1)</script>", 0),
		}},
		{Workload: "other", Kind: "kv", Name: "theirs"},
		{Workload: "app", Kind: "r2", Name: "files", Entries: []fakemgmt.StoreSnapshotEntry{{Seq: 1}}},
		{Workload: "app", Kind: "d1", Name: "app-db"},
	}
	c := &devConsole{
		workload: "app",
		appURL:   "http://127.0.0.1:8787",
		stores:   func() []fakemgmt.StoreSnapshot { return snaps },
		d1:       fakeD1(t, seen),
		now:      func() time.Time { return now },
	}
	c.setD1([]devD1{{Binding: "DB", Database: "app-db", Applied: []string{"0001_init.sql"}}})
	return c, c.handler(4000)
}

func consoleGet(t *testing.T, h http.Handler, method, target, host string) (int, string, http.Header) {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b), w.Header()
}

// TestDevConsole: tw dev's read-only data console lists the run's KV
// namespaces and D1 databases, pages KV keys (folded: deletes and expired
// keys gone), shows values, lists D1 tables and rows, escapes what the app
// wrote, and refuses writes and foreign Host headers.
func TestDevConsole(t *testing.T) {
	t.Parallel()
	var seen []string
	_, h := devConsoleForTest(t, &seen)
	const host = "127.0.0.1:4000"

	code, body, hd := consoleGet(t, h, "GET", "/", host)
	if code != 200 || !strings.Contains(body, `href="/kv/cache"`) || !strings.Contains(body, `href="/d1/app-db"`) || !strings.Contains(body, "files") {
		t.Fatalf("overview %d:\n%s", code, body)
	}
	if strings.Contains(body, "theirs") {
		t.Errorf("overview lists another workload's namespace")
	}
	if !strings.Contains(hd.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("CSP = %q", hd.Get("Content-Security-Policy"))
	}

	code, body, _ = consoleGet(t, h, "GET", "/kv/cache", host)
	if code != 200 {
		t.Fatalf("kv keys %d:\n%s", code, body)
	}
	for _, want := range []string{"user:1", "user:2", "bin", "&lt;script&gt;", ">binary<"} {
		if !strings.Contains(body, want) {
			t.Errorf("kv keys: no %q in\n%s", want, body)
		}
	}
	for _, gone := range []string{">gone<", ">expired<", "<script>alert"} {
		if strings.Contains(body, gone) {
			t.Errorf("kv keys: %q shown", gone)
		}
	}
	code, body, _ = consoleGet(t, h, "GET", "/kv/cache?prefix=user:", host)
	if code != 200 || strings.Contains(body, ">bin<") || !strings.Contains(body, "user:2") {
		t.Errorf("kv prefix %d:\n%s", code, body)
	}

	code, body, _ = consoleGet(t, h, "GET", "/kv/cache/value?key=user:1", host)
	if code != 200 || !strings.Contains(body, "&#34;name&#34;: &#34;Ada&#34;") {
		t.Errorf("kv value %d:\n%s", code, body)
	}
	code, body, _ = consoleGet(t, h, "GET", "/kv/cache/value?key=bin", host)
	if code != 200 || !strings.Contains(body, "ff fe") {
		t.Errorf("binary kv value %d:\n%s", code, body)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/kv/cache/value?key=gone", host); code != 404 {
		t.Errorf("deleted key: %d", code)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/kv/theirs", host); code != 404 {
		t.Errorf("another workload's namespace: %d", code)
	}

	code, body, _ = consoleGet(t, h, "GET", "/d1/app-db", host)
	if code != 200 || !strings.Contains(body, `href="/d1/app-db/notes"`) || !strings.Contains(body, "<td>2</td>") {
		t.Errorf("d1 tables %d:\n%s", code, body)
	}
	code, body, _ = consoleGet(t, h, "GET", "/d1/app-db/notes", host)
	if code != 200 || !strings.Contains(body, "&lt;b&gt;hello&lt;/b&gt;") || !strings.Contains(body, "NULL") || !strings.Contains(body, ">body<") {
		t.Errorf("d1 rows %d:\n%s", code, body)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/d1/app-db/nope", host); code != 404 {
		t.Errorf("unknown table: %d", code)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/d1/other-db", host); code != 404 {
		t.Errorf("unbound database: %d", code)
	}
	for _, q := range seen {
		if !strings.HasPrefix(q, "SELECT ") {
			t.Errorf("console ran a non-SELECT: %q", q)
		}
	}

	if code, _, _ = consoleGet(t, h, "POST", "/kv/cache", host); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", code)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/", "evil.example:4000"); code != http.StatusMisdirectedRequest {
		t.Errorf("foreign Host: %d", code)
	}
	if code, _, _ = consoleGet(t, h, "GET", "/", "localhost:4000"); code != 200 {
		t.Errorf("localhost Host: %d", code)
	}
	if code, body, _ = consoleGet(t, h, "GET", "/static/app.css", host); code != 200 || !strings.Contains(body, "--bg") {
		t.Errorf("app.css %d", code)
	}
}

// TestDevConsoleListen: --console-port -1 serves nothing; 0 picks a port.
func TestDevConsoleListen(t *testing.T) {
	t.Parallel()
	u, stop, err := startDevConsole(-1, nil)
	stop()
	if err != nil || u != "" {
		t.Fatalf("-1: %q %v", u, err)
	}
	var seen []string
	c, _ := devConsoleForTest(t, &seen)
	u, stop, err = startDevConsole(0, c)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatalf("url %q", u)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", u, resp.StatusCode)
	}
}

// TestDevHotkeyConsole: e opens the console, as in wrangler dev.
func TestDevHotkeyConsole(t *testing.T) {
	t.Parallel()
	if devHotkey('e') != hotkeyConsole || devHotkey('E') != hotkeyConsole {
		t.Fatal("e is not the console hotkey")
	}
}
