package mgmt

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Issue #512, store side: ObjectFilter.Tag/ActingFor and AuditFilter.Workload,
// and the audit.workload backfill in migration 202611251900_audit_workload.
// SQLite always; Postgres with TW_TEST_POSTGRES_DSN (make test-pg).
func TestStoreAuditWorkloadAndObjectFilters(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		for _, o := range []Object{
			{ID: "obj_a", TenantID: "tn", Workload: "web", Namespace: "A", Name: "a", Tags: []string{"tag:ab", "tag:x"}, ActingFor: "pat@example.com", Status: StatusPending, CreatedAt: 1},
			{ID: "obj_b", TenantID: "tn", Workload: "api", Namespace: "A", Name: "b", Tags: []string{"tag:a"}, Status: StatusPending, CreatedAt: 2},
		} {
			if err := s.CreateObject(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		objs := func(f ObjectFilter) []string {
			t.Helper()
			os, err := s.ListObjects(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, o := range os {
				ids = append(ids, o.ID)
			}
			return ids
		}
		if got := objs(ObjectFilter{Tag: "tag:a"}); !slices.Equal(got, []string{"obj_b"}) {
			t.Fatalf("tag:a = %v (tag:ab must not match)", got)
		}
		if got := objs(ObjectFilter{Tag: "tag:x", ActingFor: "pat@example.com"}); !slices.Equal(got, []string{"obj_a"}) {
			t.Fatalf("tag:x + human = %v", got)
		}
		if got := objs(ObjectFilter{ActingFor: "PAT@example.com"}); len(got) != 0 {
			t.Fatalf("human is exact: %v", got)
		}

		appendRec := func(r AuditRecord) {
			t.Helper()
			if _, err := s.AppendAudit(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		appendRec(AuditRecord{Chain: "mgmt", TS: "2026-11-25T00:00:01Z", TenantID: "tn", ObjectID: "obj_a", Kind: "create", Detail: json.RawMessage(`{"workload":"api"}`)})
		appendRec(AuditRecord{Chain: "mgmt", TS: "2026-11-25T00:00:02Z", TenantID: "tn", Kind: "deploy", Detail: json.RawMessage(`{"workload":"api"}`)})
		appendRec(AuditRecord{Chain: "mgmt", TS: "2026-11-25T00:00:03Z", TenantID: "tn", Kind: "tenant_create", Detail: json.RawMessage(`"no object"`)})
		// A host record without an object: its detail is never trusted for the workload.
		appendRec(AuditRecord{Chain: "host:h1", TS: "2026-11-25T00:00:04Z", TenantID: "tn", Kind: "tenant_note", Hash: "h1",
			Raw: json.RawMessage(`{"kind":"tenant_note","detail":{"workload":"api"}}`)})
		check := func(when string) {
			t.Helper()
			byWorkload := map[string][]string{}
			for _, w := range []string{"web", "api", ""} {
				recs, err := s.ListAudit(ctx, AuditFilter{Workload: w})
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range recs {
					if w != "" {
						byWorkload[w] = append(byWorkload[w], r.Kind)
					}
				}
			}
			if !slices.Equal(byWorkload["web"], []string{"create"}) || !slices.Equal(byWorkload["api"], []string{"deploy"}) {
				t.Fatalf("%s: by workload %v (the object's workload wins; host detail never counts)", when, byWorkload)
			}
		}
		check("at append")

		// The migration's backfill derives the same values.
		if _, err := s.db.ExecContext(ctx, "DELETE FROM audit_workloads"); err != nil {
			t.Fatal(err)
		}
		var m Migration
		for _, x := range Migrations() {
			if x.ID == "202611251900_audit_workload" {
				m = x
			}
		}
		n := 0
		for _, stmt := range splitSQL(stripSQLComments(m.sql(s.d))) {
			if strings.HasPrefix(strings.TrimSpace(stmt), "INSERT") {
				if _, err := s.db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("%v: %s", err, stmt)
				}
				n++
			}
		}
		if n != 2 {
			t.Fatalf("backfill statements: %d", n)
		}
		check("after backfill")
	})
}
