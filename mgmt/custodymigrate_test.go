package mgmt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateFileCustodyToSetec (issue #295): an install whose custody
// entries were written before custody_names existed moves them to setec.
// The backfill indexes every derivable name (a never-deployed workload's
// secrets through the audit chain); an entry no name maps to is refused,
// and so is a setec that holds another value under one of the names;
// after the copy every entry reads back the same from setec, and running
// it again copies nothing.
func TestMigrateFileCustodyToSetec(t *testing.T) {
	ctx := context.Background()
	w := newTenantWorld(t)
	e := w.e
	tn := w.populate(t, "a.ts.net")
	w.populate(t, "b.ts.net")
	fc := w.cust.Custody.(*FileCustody)
	if _, err := LoadSharedIDSecret(ctx, filepath.Join(t.TempDir(), "id.secret"), e.store, fc); err != nil {
		t.Fatal(err)
	}
	// Before custody_names: nothing is indexed.
	if _, err := e.store.db.ExecContext(ctx, "DELETE FROM custody_names"); err != nil {
		t.Fatal(err)
	}
	// An entry nothing names (an object row gone, its key left behind).
	orphan := "tw/" + tn + "/o_gone/authkey"
	fc.Put(ctx, orphan, []byte("k-orphan"))
	files, _, err := fc.files()
	if err != nil {
		t.Fatal(err)
	}

	srv := fakeSetec(t)
	defer srv.Close()
	setec := &SetecCustody{BaseURL: srv.URL}
	now := e.m.now().UnixMilli()

	// Unmapped: refused, and a dry run writes nothing (not even the index).
	rep, err := MigrateFileCustody(ctx, e.store, fc, setec, now, true)
	if !errors.Is(err, ErrCustodyUnmapped) || len(rep.Unmapped) != 1 || rep.Unmapped[0] != fc.fileName(orphan) {
		t.Fatalf("unmapped: %v %v", rep.Unmapped, err)
	}
	if idx, _ := e.store.IndexedCustodyNames(ctx); len(idx) != 0 || len(rep.Backfilled) < 16 {
		t.Fatalf("dry run: indexed %v, would backfill %v", idx, rep.Backfilled)
	}
	// Real run, still unmapped: refused before setec is written; the
	// backfill (harmless) is kept.
	if _, err := MigrateFileCustody(ctx, e.store, fc, setec, now, false); !errors.Is(err, ErrCustodyUnmapped) {
		t.Fatal(err)
	}
	if _, err := setec.Get(ctx, secretsCustodyName(tn, "support")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("setec written despite the refusal: %v", err)
	}
	idx, _ := e.store.IndexedCustodyNames(ctx)
	for _, want := range []string{secretsCustodyName(tn, "never-deployed"), secretsCustodyName(tn, "support"), volumeKeyName(tn, "support"),
		notifyCustodyName(tn), telemetryCustodyName(tn)} {
		if !containsName(idx, want) {
			t.Errorf("backfill: %s not indexed (%v)", want, idx)
		}
	}
	// The operator moves the orphan aside.
	if err := os.Rename(filepath.Join(fc.SecretsDir(), fc.fileName(orphan)), filepath.Join(t.TempDir(), "orphan")); err != nil {
		t.Fatal(err)
	}
	// A setec that already holds another value under one name: refused.
	setec.Put(ctx, secretsCustodyName(tn, "support"), []byte("not the same"))
	rep, err = MigrateFileCustody(ctx, e.store, fc, setec, now, false)
	if !errors.Is(err, ErrCustodyConflict) || len(rep.Conflicts) != 1 || rep.Copied != 0 {
		t.Fatalf("conflict: %+v %v", rep, err)
	}
	setec.Delete(ctx, secretsCustodyName(tn, "support"))

	rep, err = MigrateFileCustody(ctx, e.store, fc, setec, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != len(files)-1 || len(rep.Names) != rep.Files || rep.Copied != rep.Files || rep.Verified != rep.Files || len(rep.Backfilled) != 0 {
		t.Fatalf("migration: %d files before, %+v", len(files), rep)
	}
	for _, n := range rep.Names {
		want, err := fc.Get(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := setec.Get(ctx, n); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("setec %s: %v", n, err)
		}
	}
	kid, err := e.store.SecretChoice(ctx, idSecretChoice)
	if err != nil || !containsName(rep.Names, idSecretCustodyName(kid)) {
		t.Fatalf("ID secret not migrated: %v %v", rep.Names, err)
	}
	// Every name but the ID secret is now indexed.
	if idx, _ := e.store.IndexedCustodyNames(ctx); len(idx) != rep.Files-1 {
		t.Fatalf("indexed %d of %d", len(idx), rep.Files)
	}
	// Mgmt on setec reads what it wrote on file custody.
	if _, err := LoadSharedIDSecret(ctx, filepath.Join(t.TempDir(), "id.secret"), e.store, setec); err != nil {
		t.Fatal(err)
	}

	// Again (after an interruption, say): nothing to copy.
	rep, err = MigrateFileCustody(ctx, e.store, fc, setec, now, false)
	if err != nil || rep.Copied != 0 || rep.Present != rep.Files || rep.Verified != rep.Files {
		t.Fatalf("second run: %+v %v", rep, err)
	}
}
