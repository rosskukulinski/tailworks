package mgmt

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestAuditPruneKeepsChainVerifiable (#120): pruning a chain's oldest
// records moves its checkpoint; what remains still verifies, a later
// edit, gap or truncation among the kept records is still caught, and
// appends after a prune link on as before.
func TestAuditPruneKeepsChainVerifiable(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		appendN := func(from, n int) {
			for i := from; i < from+n; i++ {
				if _, err := s.AppendAudit(ctx, AuditRecord{Chain: "mgmt", TS: fmt.Sprintf("2026-01-01T00:00:%02dZ", i), Kind: "spawn", ObjectID: fmt.Sprintf("o%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		appendN(0, 10)
		// Keep the newest 4: records 1..6 go, in batches of 4.
		through, err := s.AuditPruneTarget(ctx, "mgmt", "", 4)
		if err != nil || through != 6 {
			t.Fatalf("target by count: %d %v", through, err)
		}
		n, cp, err := s.PruneAudit(ctx, "mgmt", through, 4, 1)
		if err != nil || n != 4 || cp.Seq != 4 || cp.Pruned != 4 || cp.Hash == "" {
			t.Fatalf("first batch: %d %+v %v", n, cp, err)
		}
		if st := VerifyChain(ctx, s, "mgmt", nil); !st.OK || st.Records != 6 || st.PrunedThrough != 4 {
			t.Fatalf("after one batch: %+v", st)
		}
		if n, cp, err = s.PruneAudit(ctx, "mgmt", through, 4, 2); err != nil || n != 2 || cp.Seq != 6 || cp.Pruned != 6 {
			t.Fatalf("second batch: %d %+v %v", n, cp, err)
		}
		if n, _, err := s.PruneAudit(ctx, "mgmt", through, 4, 3); err != nil || n != 0 {
			t.Fatalf("nothing left to prune: %d %v", n, err)
		}
		appendN(10, 2) // links on to record 10
		st := VerifyChain(ctx, s, "mgmt", nil)
		if !st.OK || st.Records != 6 || st.PrunedThrough != 6 {
			t.Fatalf("after the prune and two appends: %+v", st)
		}
		// By age: the records before 00:00:09 go (seq ≤ 9); 00:00:09 stays.
		if through, err := s.AuditPruneTarget(ctx, "mgmt", "2026-01-01T00:00:09Z", 0); err != nil || through != 9 {
			t.Fatalf("target by age: %d %v", through, err)
		}
		// The guard still refuses deleting a record the checkpoint doesn't
		// cover, and any update.
		if err := s.rawExec(ctx, "DELETE FROM audit WHERE seq=8"); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("delete past the checkpoint allowed: %v", err)
		}
		if err := s.rawExec(ctx, "UPDATE audit SET kind='allow' WHERE seq=8"); err == nil {
			t.Fatal("update allowed")
		}
		// Tampering with the kept records is still caught.
		dropGuards(t, s)
		if err := s.rawExec(ctx, `UPDATE audit SET raw=REPLACE(raw, '"kind":"spawn"', '"kind":"allow"') WHERE seq=8`); err != nil {
			t.Fatal(err)
		}
		if st := VerifyChain(ctx, s, "mgmt", nil); st.OK || st.BrokenAt != 8 {
			t.Fatalf("edit after a prune not detected: %+v", st)
		}
		// So is deleting the first kept record without moving the checkpoint.
		s.rawExec(ctx, "DELETE FROM audit WHERE seq=7")
		if st := VerifyChain(ctx, s, "mgmt", nil); st.OK || st.BrokenAt != 8 {
			t.Fatalf("gap after the checkpoint not detected: %+v", st)
		}
	})
}

// TestAuditRetentionPruner (#120): with -audit-max-records the pruner
// prunes every chain, records the prune on the mgmt chain, and verify
// stays green; without a retention setting nothing is deleted.
func TestAuditRetentionPruner(t *testing.T) {
	t.Parallel()
	e := newTestEnvWith(t, func(c *Config) { c.AuditMaxRecords = 3 })
	ctx := context.Background()
	for i := range 8 {
		e.m.audit(ctx, AuditRecord{Kind: "tenant_note", TenantID: fmt.Sprintf("t%d", i)}, nil)
	}
	got, err := e.m.PruneAudit(ctx)
	if err != nil || got["mgmt"] < 5 {
		t.Fatalf("prune: %v %v", got, err)
	}
	st := e.m.VerifyChain(ctx, "mgmt")
	if !st.OK || st.PrunedThrough == 0 || st.Records != 4 { // the 3 kept and the audit_pruned record
		t.Fatalf("verify after the prune: %+v", st)
	}
	recs, _ := e.store.ListAudit(ctx, AuditFilter{Chain: "mgmt", Kinds: []string{"audit_pruned"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"chain":"mgmt"`) || !strings.Contains(string(recs[0].Detail), `"maxRecords":3`) {
		t.Fatalf("audit_pruned record: %+v", recs)
	}
	var v struct {
		OK     bool          `json:"ok"`
		Chains []ChainStatus `json:"chains"`
	}
	e.c.ok("GET", "/v1/audit/verification", nil, &v)
	if !v.OK || len(v.Chains) == 0 || v.Chains[0].PrunedThrough == 0 {
		t.Fatalf("GET verification: %+v", v)
	}

	// Off by default: nothing is pruned.
	e2 := newTestEnv(t)
	for range 5 {
		e2.m.audit(ctx, AuditRecord{Kind: "tenant_note"}, nil)
	}
	if got, err := e2.m.PruneAudit(ctx); err != nil || len(got) != 0 {
		t.Fatalf("default retention pruned: %v %v", got, err)
	}
}
