package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCustodyMigrateCLI (issue #295): tw custody migrate refuses an entry
// it can't name and leaves tw.json alone; once that entry is moved aside,
// it copies the rest to setec, keeps the state from before, and switches
// tw.json to setec custody, which tw up then keeps.
func TestCustodyMigrateCLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &state{Dir: dir}
	s.Cfg.Tailnet = "t.ts.net"
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "mgmt"), 0o700)
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	fc, err := mgmt.OpenFileCustody(filepath.Join(dir, "mgmt", "custody"))
	if err != nil {
		t.Fatal(err)
	}
	const name = "tw/tn_a/notifications"
	st.IndexCustodyName(ctx, name, "tn_a", 1)
	fc.Put(ctx, name, []byte(`{"webhook":"x"}`))
	fc.Put(ctx, "tw/tn_a/o_gone/authkey", []byte("orphan"))
	st.Close()

	var mu sync.Mutex
	vals := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name  string
			Value []byte
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/put":
			vals[req.Name] = req.Value
			json.NewEncoder(w).Encode(1)
		case "/api/activate":
		case "/api/get":
			v, ok := vals[req.Name]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Value": v, "Version": 1})
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	run := func() error {
		return cmdCustody([]string{"migrate", "--setec-url", srv.URL, "--state-dir", dir})
	}
	cfg := func() Config {
		t.Helper()
		s, err := openState(dir)
		if err != nil {
			t.Fatal(err)
		}
		return s.Cfg
	}

	for _, bad := range [][]string{{"migrate"}, {"migrate", "--to", "vault", "--setec-url", srv.URL}, {"migrate", "--to", "setec"}, {"move"}} {
		if err := cmdCustody(append(bad, "--state-dir", dir)); err == nil || asCLIErr(err).Code != "bad_request" {
			t.Errorf("%v: %v", bad, err)
		}
	}
	err = run()
	if err == nil || asCLIErr(err).Code != "custody_unmapped" {
		t.Fatalf("unmapped entry: %v", err)
	}
	if c := cfg(); c.Custody != "" || len(vals) != 0 {
		t.Fatalf("after the refusal: custody %q, setec %v", c.Custody, vals)
	}
	// The error lists the file; the operator moves it aside.
	lines := asCLIErr(err).Lines
	if len(lines) < 2 {
		t.Fatalf("no file listed: %v", lines)
	}
	orphan := strings.TrimSpace(lines[1])
	if err := os.Rename(filepath.Join(fc.SecretsDir(), orphan), filepath.Join(t.TempDir(), orphan)); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if c := cfg(); c.Custody != "setec" || c.SetecURL != srv.URL {
		t.Fatalf("tw.json: %+v", c)
	}
	if string(vals[name]) != `{"webhook":"x"}` || len(vals) != 1 {
		t.Fatalf("setec: %v", vals)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "*-before-custody-migrate", "tw.json"))
	if len(backups) != 1 {
		t.Fatalf("no state kept from before: %v", backups)
	}
	// tw up keeps setec custody (no custody switch refused).
	f := newFlags("up")
	sf := addStoreFlags(f)
	f.parse(nil)
	s2, _ := openState(dir)
	keepSavedUpFlags(f.FlagSet, s2.Cfg)
	if err := sf.apply(s2); err != nil || s2.Cfg.Custody != "setec" {
		t.Fatalf("tw up after the move: %v %q", err, s2.Cfg.Custody)
	}
	// Again: already on setec.
	if err := run(); err == nil || asCLIErr(err).Code != "state_conflict" {
		t.Fatalf("second run: %v", err)
	}
}
