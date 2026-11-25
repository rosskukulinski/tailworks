package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCLID1Repair (#147): tw d1 list shows a degraded database and how to
// repair it; tw d1 repair asks first (--yes), refuses the wrong entry, and
// skips the right one (audited); the listing is clean after.
func TestCLID1Repair(t *testing.T) {
	e := newPubEnv(t)
	app := t.TempDir()
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "support", "tailnet": { "tags": ["tag:tw-wl-support"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "d1_databases": [{ "binding": "DB", "database_name": "app-db", "database_id": "6c1a…" }]}`), 0o644)
	t.Chdir(app)
	ctx := context.Background()

	res, err := e.m.AppendStore(ctx, e.tenantID, "support", "d1", "app-db", "", nil, 0, "", []mgmt.StoreEntryIn{{Data: []byte(`{"v":1}`)}, {Data: []byte(`{"v":1,"bad":1}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.ReportStoreStatus(ctx, e.tenantID, "support", "d1", "app-db", "", nil, res.Incarnation, 1, &mgmt.StoreDegraded{Seq: 2, Error: "no such table: gone", At: 1}); err != nil {
		t.Fatal(err)
	}
	out, err := e.call(t, cmdD1, "list")
	if err != nil || !strings.Contains(out, "degraded: journal entry 2 failed to apply") || !strings.Contains(out, "no such table: gone") || !strings.Contains(out, "tw d1 repair app-db --skip 2 --workload support") {
		t.Fatalf("list of a degraded database: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdD1, "list", "--json"); err != nil || !strings.Contains(out, `"degraded": {`) {
		t.Fatalf("list --json: %v\n%s", err, out)
	}

	if _, err := e.call(t, cmdD1, "repair", "DB"); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "--skip") {
		t.Fatalf("repair without --skip: %v", err)
	}
	if _, err := e.call(t, cmdD1, "repair", "DB", "--skip", "2"); cliCode(err) != "confirmation_required" || !strings.Contains(cliText(err), "tw d1 repair app-db --skip 2 --yes") {
		t.Fatalf("repair without --yes: %v", err)
	}
	if _, err := e.call(t, cmdD1, "repair", "DB", "--skip", "1", "--yes"); cliCode(err) != "store_not_degraded" || !strings.Contains(cliText(err), "degraded at journal entry 2, not 1") {
		t.Fatalf("repair of the wrong entry: %v", err)
	}
	out, err = e.call(t, cmdD1, "repair", "DB", "--skip", "2", "--yes", "--json")
	var r d1Result
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Skipped != 2 || r.Repaired == nil || r.Repaired.Degraded != nil || r.Repaired.Entries != 1 {
		t.Fatalf("repair: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdD1, "list"); err != nil || strings.Contains(out, "degraded") {
		t.Fatalf("list after the repair: %v\n%s", err, out)
	}
	recs, _ := e.st.ListAudit(ctx, mgmt.AuditFilter{TenantID: e.tenantID, Kinds: []string{"store_repair"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"seq":2`) || !strings.Contains(string(recs[0].Detail), "tw cli") {
		t.Fatalf("store_repair audit: %+v", recs)
	}
}
