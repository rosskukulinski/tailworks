package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/mgmtd"
)

// TestUpStoreFlags: tw up's --db / --db-file / --custody / --setec-url
// (issue #120) are checked before tw up changes anything, reach tw-mgmt
// (separate layout) and the embedded mgmt (single layout), survive a
// later tw up that leaves them out, and never switch the custody of an
// install that has secrets.
func TestUpStoreFlags(t *testing.T) {
	dir := t.TempDir()
	dsnFile := filepath.Join(dir, "pg.dsn")
	os.WriteFile(dsnFile, []byte("postgres://tw:pw@db:5432/tw\n"), 0o600)
	loose := filepath.Join(dir, "loose.dsn")
	os.WriteFile(loose, []byte("postgres://tw:pw@db:5432/tw\n"), 0o644)
	parse := func(args ...string) (*flags, storeFlags) {
		t.Helper()
		f := newFlags("up")
		sf := addStoreFlags(f)
		if _, err := f.parse(args); err != nil {
			t.Fatal(err)
		}
		return f, sf
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--db", "/tmp/x.db"}, "postgres://"},
		{[]string{"--db", "postgres://a", "--db-file", dsnFile}, "not both"},
		{[]string{"--db-file", loose}, "credential_file_insecure"},
		{[]string{"--db-file", filepath.Join(dir, "none")}, "no such file"},
		{[]string{"--custody", "setec"}, "--setec-url"},
		{[]string{"--setec-url", "https://s"}, "--custody setec"},
		{[]string{"--custody", "vault"}, "file or setec"},
	} {
		_, sf := parse(c.args...)
		if _, err := sf.validate(); err == nil || !strings.Contains(asCLIErr(err).Code+" "+err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}

	// A new install on Postgres and setec custody.
	s := &state{Dir: filepath.Join(dir, "state")}
	_, sf := parse("--db-file", dsnFile, "--custody", "setec", "--setec-url", "https://setec.example")
	if err := sf.apply(s); err != nil {
		t.Fatal(err)
	}
	if got := s.Cfg.storeMgmtArgs(); !slices.Equal(got, []string{"-db-file", dsnFile, "-custody", "setec", "-setec-url", "https://setec.example"}) {
		t.Fatalf("tw-mgmt args %v", got)
	}
	var o mgmtd.Options
	s.Cfg.storeOptions(&o)
	if o.DBFile != dsnFile || o.DB != "" || o.Custody != "setec" || o.SetecURL != "https://setec.example" {
		t.Fatalf("embedded mgmt options %+v", o)
	}
	if !strings.Contains(s.Cfg.storeLabel(s.Dir), "Postgres") || !strings.Contains(s.Cfg.storeLabel(s.Dir), "setec") {
		t.Fatalf("label %q", s.Cfg.storeLabel(s.Dir))
	}
	// tw up again without the flags keeps them (up_saved.go).
	f, sf := parse()
	keepSavedUpFlags(f.FlagSet, s.Cfg)
	if err := sf.apply(s); err != nil || s.Cfg.DBFile != dsnFile || s.Cfg.Custody != "setec" {
		t.Fatalf("kept: %v %+v", err, s.Cfg)
	}
	// setec → file is refused.
	_, sf = parse("--db-file", dsnFile, "--custody", "file")
	if err := sf.apply(s); err == nil || asCLIErr(err).Code != "state_conflict" {
		t.Fatalf("setec → file: %v", err)
	}

	// The default: SQLite, file custody, no extra flags.
	d := &state{Dir: filepath.Join(dir, "default")}
	_, sf = parse()
	if err := sf.apply(d); err != nil || len(d.Cfg.storeMgmtArgs()) != 0 || !d.Cfg.storeIsSQLite() {
		t.Fatalf("default: %v %v", err, d.Cfg.storeMgmtArgs())
	}
	// file → setec on an install that has secrets is refused.
	os.MkdirAll(filepath.Join(d.Dir, "mgmt", "custody", "secrets"), 0o700)
	os.WriteFile(filepath.Join(d.Dir, "mgmt", "custody", "secrets", "abc"), []byte("x"), 0o600)
	_, sf = parse("--custody", "setec", "--setec-url", "https://s")
	if err := sf.apply(d); err == nil || asCLIErr(err).Code != "state_conflict" {
		t.Fatalf("file → setec with secrets: %v", err)
	}
	// The fix is the exact command, with the URL given (issue #444).
	if fix := strings.Join(asCLIErr(sf.apply(d)).Fix, "\n"); !strings.Contains(fix, "tw custody migrate --setec-url https://s --state-dir "+d.Dir) || strings.Contains(fix, "--to") {
		t.Fatalf("file → setec fix: %s", fix)
	}
}
