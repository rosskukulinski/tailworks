package mgmt

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestMigrationFiles(t *testing.T) {
	t.Parallel()
	ms := Migrations()
	if len(ms) == 0 {
		t.Fatal("no embedded migrations")
	}
	if !slices.IsSortedFunc(ms, func(a, b Migration) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	}) {
		t.Fatal("migrations not in ID order")
	}
	for _, m := range ms {
		if !validMigrationID(m.ID) {
			t.Errorf("bad ID %q", m.ID)
		}
		for _, d := range []dialect{dialectSQLite, dialectPostgres} {
			if len(splitSQL(stripSQLComments(m.sql(d)))) == 0 {
				t.Errorf("%s: no statements", m.ID)
			}
		}
	}
	for _, bad := range []string{"1_x", "20261115190_x", "202611151900-x", "202611151900_X"} {
		if validMigrationID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func openTestStore(t *testing.T) (*SQLStore, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "mgmt.db")
	s, err := OpenStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	return s, dsn
}

func TestSchemaStatusAfterOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dsn := openTestStore(t)
	st, err := s.SchemaStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ms := Migrations()
	if st.Applied != ms[len(ms)-1].ID || st.Latest != st.Applied || len(st.Pending) != 0 || len(st.Ahead) != 0 {
		t.Fatalf("after open: %+v", st)
	}
	s.Close()
	// Reopening applies nothing twice (the migrations are not idempotent
	// on their own: ALTER TABLE ADD COLUMN fails on a second run).
	s2, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
	// --check on an up-to-date store: nothing pending.
	if st, err := CheckSchema(ctx, dsn); err != nil || len(st.Pending) != 0 {
		t.Fatalf("check: %+v %v", st, err)
	}
}

func TestSchemaAheadAndDowngradeGuard(t *testing.T) {
	ctx := context.Background()
	s, dsn := openTestStore(t)
	// A newer tw-mgmt applied an expand migration this binary doesn't know.
	s.rawExec(ctx, "INSERT INTO schema_migrations (id, applied_at, applied_by) VALUES ('299901010000_future', 1, '9.0.0')")
	s.Close()
	s, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatalf("an expand migration from a newer tw-mgmt must not block an older one: %v", err)
	}
	st, _ := s.SchemaStatus(ctx)
	if !slices.Equal(st.Ahead, []string{"299901010000_future"}) {
		t.Fatalf("ahead: %+v", st)
	}
	// …but a contract migration raises the minimum binary.
	s.rawExec(ctx, "INSERT INTO schema_meta (k, v) VALUES ('min_binary_version', '9.0.0')")
	s.Close()
	old := StoreBinaryVersion
	StoreBinaryVersion = "0.2.0"
	defer func() { StoreBinaryVersion = old }()
	if _, err := OpenStore(ctx, dsn); err == nil || !errors.As(err, new(errSchemaTooNew)) {
		t.Fatalf("open by an older binary than min_binary_version: %v", err)
	}
	if _, err := CheckSchema(ctx, dsn); err == nil {
		t.Fatal("check by an older binary: no error")
	}
	// Developer builds are never refused.
	StoreBinaryVersion = "dev"
	s, err = OpenStore(ctx, dsn)
	if err != nil {
		t.Fatalf("dev build: %v", err)
	}
	s.Close()
}

func TestContractMigrationsWaitForOlderReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := openTestStore(t)
	defer s.Close()
	contract := Migration{ID: "299912312359_drop_thing", Contract: true, common: "CREATE TABLE t_contract_probe (x TEXT)"}
	known := append(Migrations(), contract)
	// An older replica is live: the contract migration waits.
	s.UpsertReplica(ctx, Replica{ID: "rp_old", Custody: "x", StartedAt: 1, LastSeen: time.Now().UnixMilli(), Version: "0.1.0"})
	if !s.olderReplicaLive(ctx, "0.2.0") {
		t.Fatal("older replica not seen")
	}
	if err := s.applyMigrations(ctx, known, "0.2.0", !s.olderReplicaLive(ctx, "0.2.0")); err != nil {
		t.Fatal(err)
	}
	applied, meta, _ := readSchema(ctx, s.db)
	if applied[contract.ID] || meta[metaMinBinary] != "" {
		t.Fatalf("contract migration ran with an older replica live: %v %v", applied, meta)
	}
	st := schemaStatusOf(known, applied, meta)
	if !slices.Equal(st.PendingContract, []string{contract.ID}) {
		t.Fatalf("pending contract: %+v", st)
	}
	// Everyone upgraded: it runs and raises the minimum binary.
	if err := s.applyMigrations(ctx, known, "0.2.0", true); err != nil {
		t.Fatal(err)
	}
	applied, meta, _ = readSchema(ctx, s.db)
	if !applied[contract.ID] || meta[metaMinBinary] != "0.2.0" {
		t.Fatalf("after: %v %v", applied, meta)
	}
}

func TestRestoreFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dsn := openTestStore(t)
	l, err := s.AcquireLease(ctx, "t/w", "h1", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for want := int64(1); want <= 2; want++ {
		gen, err := RestoreFenceDSN(ctx, dsn)
		if err != nil || gen != want {
			t.Fatalf("restore fence %d: %d %v", want, gen, err)
		}
	}
	s, err = OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.GetLease(ctx, "t/w")
	if got.Epoch != l.Epoch+2*RestoreEpochJump {
		t.Fatalf("epoch %d, want %d", got.Epoch, l.Epoch+2*RestoreEpochJump)
	}
	if st, _ := s.SchemaStatus(ctx); st.RestoreGeneration != 2 {
		t.Fatalf("restore generation %d", st.RestoreGeneration)
	}
}
