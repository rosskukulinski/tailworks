package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestBackupRestore is tw backup and tw restore (issue #120): a backup
// taken while the store is being written (a running install) holds a
// consistent store, the custody that opens it and tw.json; changes after
// it are undone by tw restore, which keeps the state it replaced and runs
// the restore fence (lease epochs jump, restore generation 1).
func TestBackupRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &state{Dir: dir, Cfg: Config{Tailnet: "t.ts.net", HostID: "h-before", CreatedAt: "x"}}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "mgmt", "mgmt.db")
	os.MkdirAll(filepath.Join(dir, "mgmt"), 0o700)
	st, err := mgmt.OpenStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "mgmt", "custody"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cust.Put(ctx, "tw/tn/secret", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := st.AcquireLease(ctx, "tn/app", "h1", time.Hour, now); err != nil {
		t.Fatal(err)
	}

	// mgmt keeps writing while the backup runs.
	var wg sync.WaitGroup
	var stop atomic.Bool
	var written atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			if _, err := st.AcquireLease(ctx, fmt.Sprintf("tn/busy-%d", i), "h1", time.Hour, now); err != nil {
				t.Error(err)
				return
			}
			written.Add(1)
		}
	}()
	for written.Load() < 20 {
		time.Sleep(time.Millisecond)
	}
	out := filepath.Join(t.TempDir(), "bk")
	if err := cmdBackup([]string{"--state-dir", dir, "--out", out, "--json"}); err != nil {
		t.Fatal(err)
	}
	stop.Store(true)
	wg.Wait()
	var m backupManifest
	b, err := os.ReadFile(filepath.Join(out, backupManifestName))
	if err != nil || json.Unmarshal(b, &m) != nil || m.Store != "sqlite" || m.Custody != "file" || m.Schema == nil || m.Schema.Applied == "" {
		t.Fatalf("manifest: %v %s", err, b)
	}
	for _, f := range []string{"mgmt/mgmt.db", "mgmt/custody/custody.key", "tw.json"} {
		if !fileExists(filepath.Join(out, f)) {
			t.Fatalf("backup has no %s", f)
		}
	}
	if err := cmdBackup([]string{"--state-dir", dir, "--out", out, "--json"}); err == nil {
		t.Fatal("a backup into a non-empty directory was not refused")
	}

	// Changes after the backup.
	if err := cust.Put(ctx, "tw/tn/secret", []byte("v2-after")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcquireLease(ctx, "tn/after", "h1", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	s.Cfg.HostID = "h-after"
	s.save()
	st.Close() // tw down

	if err := cmdRestore([]string{out, "--state-dir", dir, "--json"}); err == nil {
		t.Fatal("restore without --yes (no terminal) was not refused")
	}
	if err := cmdRestore([]string{out, "--state-dir", dir, "--yes", "--json"}); err != nil {
		t.Fatal(err)
	}

	st2, err := mgmt.OpenStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	l, err := st2.GetLease(ctx, "tn/app")
	if err != nil || l.Epoch != 1+mgmt.RestoreEpochJump {
		t.Fatalf("lease after restore: %+v %v (want epoch 1+%d: the restore fence)", l, err, mgmt.RestoreEpochJump)
	}
	if _, err := st2.GetLease(ctx, "tn/after"); !errors.Is(err, mgmt.ErrNotFound) {
		t.Fatalf("a lease taken after the backup survived the restore: %v", err)
	}
	busy := 0
	for i := 0; i < int(written.Load()); i++ {
		if _, err := st2.GetLease(ctx, fmt.Sprintf("tn/busy-%d", i)); err == nil {
			if busy != i {
				t.Fatalf("the backup is not a prefix of the writes: busy-%d present after a gap at %d", i, busy)
			}
			busy++
		}
	}
	if busy < 20 {
		t.Fatalf("the backup holds %d of the writes made before it started (want ≥ 20)", busy)
	}
	sch, err := st2.SchemaStatus(ctx)
	if err != nil || sch.RestoreGeneration != 1 {
		t.Fatalf("schema after restore: %+v %v", sch, err)
	}
	cust2, err := mgmt.OpenFileCustody(filepath.Join(dir, "mgmt", "custody"))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := cust2.Get(ctx, "tw/tn/secret"); err != nil || string(v) != "v1" {
		t.Fatalf("custody after restore: %q %v", v, err)
	}
	s2, err := openState(dir)
	if err != nil || s2.Cfg.HostID != "h-before" {
		t.Fatalf("tw.json after restore: %+v %v", s2.Cfg, err)
	}
	// The state the restore replaced was kept.
	safety, _ := filepath.Glob(filepath.Join(dir, "backups", "*-before-restore"))
	if len(safety) != 1 {
		t.Fatalf("safety backups: %v", safety)
	}
	if b, _ := os.ReadFile(filepath.Join(safety[0], "tw.json")); !json.Valid(b) || !strings.Contains(string(b), "h-after") {
		t.Fatalf("safety backup's tw.json: %s", b)
	}
}
