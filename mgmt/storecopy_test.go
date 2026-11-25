package mgmt

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMigrateStoreCopy is `tw-mgmt migrate --from sqlite --to <dsn>`
// (issue #120): a SQLite store holding every kind of data (a store
// journal with a compacted key, queue messages, secrets indexed in
// custody_names, an audit chain pruned by retention and its checkpoint, a
// tenant storage quota, a deployment and its lease) is copied
// into an empty store, on SQLite always and on Postgres with
// TW_TEST_POSTGRES_DSN (make test-pg). Every row arrives as it was, a
// Mgmt on the target with the same custody reads the journal and the
// queue (sealed entries open), the audit chains verify, and the restore
// fence moved the epochs. A second copy into the now non-empty target, and
// a copy from a store a replica is live on, are refused.
func TestMigrateStoreCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := newQueueEnv(t)
	h1 := q.e.asHost("h1")
	journal := fmt.Sprintf("/v1/tenants/%s/workloads/support/stores/kv/sessions/journal?epoch=%d", q.tenant, q.epoch)
	var ar StoreAppendResult
	h1.ok("POST", journal, map[string]any{"after": 0, "entries": []any{entry("a", "a1"), entry("b", "b1")}}, &ar)
	h1.ok("POST", journal, map[string]any{"after": 2, "incarnation": ar.Incarnation, "entries": []any{entry("a", "a2"), entry("", "log")}}, &ar)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`{"n":1}`, "json"), qmsg("two", "text")}}, nil)
	q.e.c.ok("PUT", "/v1/tenants/"+q.tenant+"/workloads/support/secrets/API_KEY", map[string]string{"value": "s3cret"}, nil)
	// A storage quota (a tenants column) and a pruned audit chain (its
	// checkpoint in audit_checkpoints) are copied too: the pruned chain
	// verifies on the target only from that checkpoint.
	var tn Tenant
	q.e.c.ok("PATCH", "/v1/tenants/"+q.tenant, map[string]any{"maxStoreBytes": 1 << 20}, &tn)
	q.e.m.cfg.AuditMaxRecords = 2
	if pruned, err := q.e.m.PruneAudit(ctx); err != nil || pruned["mgmt"] == 0 {
		t.Fatalf("prune the source audit: %v %v", pruned, err)
	}
	q.e.m.cfg.AuditMaxRecords = 0

	src := q.e.store
	var srcPath string
	if err := src.db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&srcPath); err != nil {
		t.Fatal(err)
	}
	custodyDir := filepath.Join(filepath.Dir(srcPath), "custody")
	m := q.e.m
	wantPage, err := m.ReadStore(ctx, q.tenant, "support", "kv", "sessions", "", nil, 0, 100)
	if err != nil || len(wantPage.Entries) != 3 {
		t.Fatalf("source journal: %v %+v", err, wantPage)
	}
	wantMsgs, err := m.PeekQueue(ctx, q.tenant, "jobs", 10)
	if err != nil || len(wantMsgs) != 2 {
		t.Fatalf("source queue: %v %d", err, len(wantMsgs))
	}
	if sts, ok, err := m.VerifyAll(ctx); !ok || err != nil || !anyPruned(sts) {
		t.Fatalf("source audit: %v %v %+v", ok, err, sts)
	}

	// A replica is live on the source: refused.
	if _, err := CopySQLiteStore(ctx, srcPath, filepath.Join(t.TempDir(), "x.db")); err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("copy while mgmt runs: %v", err)
	}
	m.Close() // a clean stop: its replica row says so
	wantRows := dumpStore(t, src)

	targets := map[string]string{"sqlite": filepath.Join(t.TempDir(), "target.db")}
	if dsn := os.Getenv("TW_TEST_POSTGRES_DSN"); dsn != "" {
		p := storesUnderTest(t)["postgres"] // drops and recreates the schema
		if err := p.rawExec(ctx, "DROP SCHEMA public CASCADE"); err != nil {
			t.Fatal(err)
		}
		if err := p.rawExec(ctx, "CREATE SCHEMA public"); err != nil {
			t.Fatal(err)
		}
		targets["postgres"] = dsn
	}
	for name, dst := range targets {
		t.Run(name, func(t *testing.T) {
			rep, err := CopySQLiteStore(ctx, srcPath, dst)
			if err != nil {
				t.Fatal(err)
			}
			for _, tbl := range []string{"store_entries", "stores", "queue_messages", "audit", "audit_checkpoints", "custody_names", "deployments", "leases", "tenants"} {
				if rep.Tables[tbl] == 0 {
					t.Errorf("copied no %s rows: %v", tbl, rep.Tables)
				}
			}
			if rep.RestoreGeneration != 1 || len(rep.Schema.Pending) != 0 {
				t.Fatalf("report: %+v", rep)
			}
			to, err := OpenStore(ctx, dst)
			if err != nil {
				t.Fatal(err)
			}
			defer to.Close()
			got := dumpStore(t, to)
			for tbl, rows := range wantRows {
				g := got[tbl]
				if tbl == "leases" || tbl == "deployments" {
					g = unfence(t, to, tbl, g)
				}
				if !reflect.DeepEqual(rows, g) {
					t.Errorf("table %s differs:\n source %v\n target %v", tbl, rows, g)
				}
			}

			cust, err := OpenFileCustody(custodyDir)
			if err != nil {
				t.Fatal(err)
			}
			m2, err := New(Config{Store: to, Custody: cust, Broker: &Broker{BaseURL: q.e.ts.srv.URL},
				IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Hour, PolicyMaxAge: time.Minute, Log: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			page, err := m2.ReadStore(ctx, q.tenant, "support", "kv", "sessions", "", nil, 0, 100)
			if err != nil || !reflect.DeepEqual(page.Entries, wantPage.Entries) || page.Head != wantPage.Head || page.Incarnation != wantPage.Incarnation {
				t.Fatalf("target journal: %v\n got %+v\nwant %+v", err, page, wantPage)
			}
			msgs, err := m2.PeekQueue(ctx, q.tenant, "jobs", 10)
			if err != nil || !reflect.DeepEqual(msgs, wantMsgs) {
				t.Fatalf("target queue: %v\n got %+v\nwant %+v", err, msgs, wantMsgs)
			}
			if sts, ok, err := m2.VerifyAll(ctx); !ok || err != nil || len(sts) == 0 || !anyPruned(sts) {
				t.Fatalf("target audit chains: %v %v %+v", ok, err, sts)
			}
			if tt, err := to.GetTenant(ctx, q.tenant); err != nil || tt.MaxStoreBytes != 1<<20 {
				t.Fatalf("target tenant quota: %v %+v", err, tt)
			}
			secrets, err := m2.ListSecrets(ctx, q.tenant, "support")
			if err != nil || len(secrets) != 1 || secrets[0].Name != "API_KEY" {
				t.Fatalf("target secrets: %v %+v", err, secrets)
			}

			// The target now holds data: a second copy is refused.
			if _, err := CopySQLiteStore(ctx, srcPath, dst); err == nil || !strings.Contains(err.Error(), "not empty") {
				t.Fatalf("copy into a non-empty store: %v", err)
			}
		})
	}
}

// anyPruned reports whether a chain verified from a retention checkpoint.
func anyPruned(sts []ChainStatus) bool {
	for _, st := range sts {
		if st.PrunedThrough > 0 {
			return true
		}
	}
	return false
}

// dumpStore is every row of every table but the schema bookkeeping, each
// row as text, sorted.
func dumpStore(t *testing.T, s *SQLStore) map[string][]string {
	t.Helper()
	ctx := context.Background()
	tables, err := s.tableColumns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for tbl, cols := range tables {
		if schemaBookkeeping[tbl] {
			continue
		}
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
		}
		sort.Strings(names)
		rows, err := s.db.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+tbl)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		var rs []string
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for i, v := range vals {
				if x, ok := v.([]byte); ok {
					v = fmt.Sprintf("%x", x)
				}
				switch x := v.(type) { // Postgres REAL is float4
				case float32:
					v = x
				case float64:
					v = float32(x)
				}
				fmt.Fprintf(&b, "%s=%v;", names[i], v)
			}
			rs = append(rs, b.String())
		}
		rows.Close()
		sort.Strings(rs)
		if len(rs) > 0 {
			out[tbl] = rs
		}
	}
	return out
}

// unfence reads tbl again with every epoch moved back by the restore
// fence's jump.
func unfence(t *testing.T, s *SQLStore, tbl string, rows []string) []string {
	t.Helper()
	if err := s.rawExec(context.Background(), "UPDATE "+tbl+" SET epoch = epoch - ?", RestoreEpochJump); err != nil {
		t.Fatal(err)
	}
	defer s.rawExec(context.Background(), "UPDATE "+tbl+" SET epoch = epoch + ?", RestoreEpochJump)
	return dumpStore(t, s)[tbl]
}
