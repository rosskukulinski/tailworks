package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

func TestUpgradeSubcommand(t *testing.T) {
	subs := []string{"fleet", "rollback", "status"}
	for _, c := range []struct {
		args     []string
		sub      string
		wantRest []string
	}{
		{[]string{"fleet", "--plan"}, "fleet", []string{"--plan"}},
		{[]string{"--state-dir", "/s", "rollback", "--yes"}, "rollback", []string{"--state-dir", "/s", "--yes"}},
		{[]string{"--state-dir=/s", "--json", "status", "up_1"}, "status", []string{"--state-dir=/s", "--json", "up_1"}},
		{[]string{"--canary", "fleet", "0.5.0"}, "", []string{"--canary", "fleet", "0.5.0"}}, // a flag's value isn't the subcommand
		{[]string{"0.5.0", "--plan"}, "", []string{"0.5.0", "--plan"}},
		{[]string{"--from", "x.tgz"}, "", []string{"--from", "x.tgz"}},
		{[]string{"--", "rollback"}, "", []string{"--", "rollback"}},
	} {
		sub, rest := upgradeSubcommand(c.args, subs...)
		if sub != c.sub || !slices.Equal(rest, c.wantRest) {
			t.Errorf("upgradeSubcommand(%q) = %q %q, want %q %q", c.args, sub, rest, c.sub, c.wantRest)
		}
	}
}

// upgradeRollbackFixture is an install.sh prefix with tw 0.5.0 installed
// over 0.4.0 (bin.prev, share.prev, VERSION.prev), the repo's install.sh
// in it, and a state dir with a SQLite store; tailworks isn't running.
func upgradeRollbackFixture(t *testing.T) (prefix, state string) {
	t.Helper()
	root := t.TempDir()
	prefix, state = filepath.Join(root, "prefix"), filepath.Join(root, "state")
	for _, d := range []string{"bin", "share/tailworks", "bin.prev", "share.prev/tailworks", "mgmt"} {
		base := prefix
		if d == "mgmt" {
			base = state
		}
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(prefix, "VERSION"), "0.5.0\n")
	write(filepath.Join(prefix, "VERSION.prev"), "0.4.0\n")
	write(filepath.Join(prefix, "bin", "tw"), "#!/bin/sh\necho 0.5.0\n")
	write(filepath.Join(prefix, "bin.prev", "tw"), "#!/bin/sh\necho 0.4.0\n")
	inst, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(prefix, "install.sh"), string(inst))
	st, err := mgmt.OpenStore(context.Background(), filepath.Join(state, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return prefix, state
}

func upgradeRollbackRun(t *testing.T, prefix, state string, args ...string) error {
	t.Helper()
	old := os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	defer func() { os.Stdout = old; devnull.Close() }()
	return cmdUpgradeRollback(append([]string{"--prefix", prefix, "--state-dir", state, "--json"}, args...))
}

func TestUpgradeRollbackBinaryOnly(t *testing.T) {
	prefix, state := upgradeRollbackFixture(t)
	if err := upgradeRollbackRun(t, prefix, state); cliCode(err) != "confirmation_required" {
		t.Fatalf("without --yes: %v, want confirmation_required", err)
	}
	if err := upgradeRollbackRun(t, prefix, state, "--yes"); err != nil {
		t.Fatal(err)
	}
	if v, pv := upgradeRollbackRead(filepath.Join(prefix, "VERSION")), upgradeRollbackRead(filepath.Join(prefix, "VERSION.prev")); v != "0.4.0" || pv != "0.5.0" {
		t.Fatalf("after the rollback VERSION %s, VERSION.prev %s", v, pv)
	}
	if ds, _ := filepath.Glob(filepath.Join(state, "backups", "*-before-rollback", "mgmt", "mgmt.db")); len(ds) != 1 {
		t.Fatalf("safety backups: %v", ds)
	}
	// 0.5.0 is now the "previous" one: not a rollback.
	if err := upgradeRollbackRun(t, prefix, state, "--yes"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "newer") {
		t.Fatalf("second rollback: %v", err)
	}
}

func TestUpgradeRollbackRestore(t *testing.T) {
	prefix, state := upgradeRollbackFixture(t)
	// No backup of 0.4.0 yet: --restore has nothing to restore.
	if err := upgradeRollbackRun(t, prefix, state, "--restore", "--yes"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "no backup of tw 0.4.0") {
		t.Fatalf("--restore without a backup: %v", err)
	}
	s, err := openState(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"20260101T000000Z-0.4.0", "20260102T000000Z-0.4.0", "20260103T000000Z-0.3.0"} {
		if _, err := backupMgmtTo(ctx, s, filepath.Join(state, "backups", name)); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := upgradeRollbackBackup(s, "", "0.4.0"); err != nil || filepath.Base(b) != "20260102T000000Z-0.4.0" {
		t.Fatalf("the backup to restore: %q %v, want the newest of 0.4.0", b, err)
	}
	// A contract migration ran since: the previous release can't open
	// the store, so the store goes back too.
	db, err := sql.Open("sqlite", filepath.Join(state, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (k, v) VALUES ('min_binary_version', '0.5.0') ON CONFLICT(k) DO UPDATE SET v = excluded.v`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if why, err := upgradeRollbackNeedsRestore(ctx, s, "0.4.0"); err != nil || !strings.Contains(why, "contract migration") {
		t.Fatalf("needs restore: %q %v", why, err)
	}
	if why, err := upgradeRollbackNeedsRestore(ctx, s, "0.5.0"); err != nil || why != "" {
		t.Fatalf("needs restore for 0.5.0: %q %v", why, err)
	}
	// Without --yes and no terminal, the store restore asks first.
	if err := upgradeRollbackRun(t, prefix, state); cliCode(err) != "confirmation_required" {
		t.Fatalf("without --yes: %v, want confirmation_required", err)
	}
	if err := upgradeRollbackRun(t, prefix, state, "--yes"); err != nil {
		t.Fatal(err)
	}
	if v := upgradeRollbackRead(filepath.Join(prefix, "VERSION")); v != "0.4.0" {
		t.Fatalf("after the rollback VERSION %s", v)
	}
	st, err := mgmt.CheckSchema(ctx, filepath.Join(state, "mgmt", "mgmt.db"))
	if err != nil || st.MinBinary != "" || st.RestoreGeneration != 1 {
		t.Fatalf("the restored store: %+v %v (want the backup's, fenced once)", st, err)
	}
}
