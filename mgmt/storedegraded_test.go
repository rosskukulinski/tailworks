package mgmt

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestStoresDegraded (#147): a host reports a store object degraded at a
// journal entry (recorded on the store, shown in its listing and the
// journal page, audited once), healthy again (cleared); a report for
// another incarnation or from a non-holder is not recorded; a repair skips
// exactly the recorded entry (store_not_degraded otherwise), so a replay
// goes on to the next one, and is audited.
func TestStoresDegraded(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	ctx := context.Background()
	c, h1 := s.e.c, s.e.asHost("h1")
	base := "/v1/tenants/" + s.tenant + "/workloads/support/stores/kv/sessions"
	status := func(epoch int64) string { return fmt.Sprintf("%s/status?epoch=%d", base, epoch) }

	var r StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("", "one"), entry("", "poison"), entry("", "three")}}, &r)
	inc := r.Incarnation
	bad := map[string]any{"seq": 2, "error": "no such table: t", "at": 1234}

	c.fail("POST", base+"/repair", map[string]any{"skip": 2}, 409, "store_not_degraded")
	h1.fail("POST", status(s.epoch+1), map[string]any{"incarnation": inc, "applied": 1, "degraded": bad}, 409, "lease_lost")
	h1.okStatus("POST", status(s.epoch), map[string]any{"incarnation": "si_other", "applied": 1, "degraded": bad}, nil)
	var st StoreInfo
	if st = getStore(c, base); st.Degraded != nil {
		t.Fatalf("a report for another incarnation was recorded: %+v", st.Degraded)
	}

	if code := h1.okStatus("POST", status(s.epoch), map[string]any{"incarnation": inc, "applied": 1, "degraded": bad}, nil); code != 204 {
		t.Fatalf("report: %d", code)
	}
	h1.okStatus("POST", status(s.epoch), map[string]any{"incarnation": inc, "applied": 1, "degraded": bad}, nil) // a repeat
	c.ok("GET", base, nil, &st)
	if d := st.Degraded; d == nil || d.Seq != 2 || d.Error != "no such table: t" || d.Host != "h1" || d.At != 1234 || d.Workload != "support" {
		t.Fatalf("degraded record: %+v", d)
	}
	var items []StoreInfo
	if c.items("/v1/tenants/"+s.tenant+"/workloads/support/stores?kind=kv", &items); len(items) != 1 || items[0].Degraded == nil {
		t.Fatalf("listing: %+v", items)
	}
	var deg struct{ Items []StoreInfo }
	if c.ok("GET", "/v1/stores/degraded?tenant="+s.tenant, nil, &deg); len(deg.Items) != 1 || deg.Items[0].Name != "sessions" {
		t.Fatalf("degraded stores: %+v", deg.Items)
	}
	var p StoreJournalPage
	if h1.ok("GET", s.journal(s.epoch, "after=1"), nil, &p); p.Degraded == nil || p.Degraded.Seq != 2 {
		t.Fatalf("journal page: %+v", p.Degraded)
	}
	recs, _ := s.e.store.ListAudit(ctx, AuditFilter{TenantID: s.tenant, Kinds: []string{"store_replay_failed"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"seq":2`) || !strings.Contains(string(recs[0].Detail), `"host":"h1"`) {
		t.Fatalf("store_replay_failed audit: %+v", recs)
	}

	// Healthy again (the entry applied on a retry): cleared.
	h1.okStatus("POST", status(s.epoch), map[string]any{"incarnation": inc, "applied": 3}, nil)
	if st = getStore(c, base); st.Degraded != nil {
		t.Fatalf("healed, still recorded: %+v", st.Degraded)
	}

	// Degraded again; the operator skips the wrong entry, then the right one.
	h1.okStatus("POST", status(s.epoch), map[string]any{"incarnation": inc, "applied": 1, "degraded": bad}, nil)
	c.fail("POST", base+"/repair", map[string]any{"skip": 3}, 409, "store_not_degraded")
	before := st
	st = StoreInfo{}
	if c.ok("POST", base+"/repair", map[string]any{"skip": 2}, &st); st.Degraded != nil || st.Head != 3 || st.Entries != before.Entries-1 || st.Bytes != before.Bytes-int64(len("poison")) {
		t.Fatalf("repaired: %+v (before %+v)", st, before)
	}
	p = StoreJournalPage{}
	h1.ok("GET", s.journal(s.epoch, "after=1"), nil, &p)
	if len(p.Entries) != 1 || p.Entries[0].Seq != 3 || p.Degraded != nil {
		t.Fatalf("journal after the repair: %v %+v", p, p.Entries)
	}
	recs, _ = s.e.store.ListAudit(ctx, AuditFilter{TenantID: s.tenant, Kinds: []string{"store_repair"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"seq":2`) || !strings.Contains(string(recs[0].Detail), "no such table") {
		t.Fatalf("store_repair audit: %+v", recs)
	}
	if c.ok("GET", "/v1/stores/degraded", nil, &deg); len(deg.Items) != 0 {
		t.Fatalf("degraded stores after the repair: %+v", deg.Items)
	}
}

// TestStoresDegradedMove: after the workload moves, the new host's healthy
// report clears the old host's record once it has applied the entry; a
// healthy report that hasn't doesn't.
func TestStoresDegradedMove(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	ctx := context.Background()
	c, h1, h2 := s.e.c, s.e.asHost("h1"), s.e.asHost("h2")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	base := "/v1/tenants/" + s.tenant + "/workloads/support/stores/kv/sessions"
	var r StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("", "one"), entry("", "two")}}, &r)
	h1.okStatus("POST", fmt.Sprintf("%s/status?epoch=%d", base, s.epoch), map[string]any{"incarnation": r.Incarnation, "applied": 1, "degraded": map[string]any{"seq": 2, "error": "x", "at": 1}}, nil)

	l, err := s.e.store.MoveLease(ctx, LeaseName(s.tenant, "support"), "h1", s.epoch, "h2", NamespaceLeaseTTL, s.e.m.now())
	if err != nil {
		t.Fatal(err)
	}
	var st StoreInfo
	h2.okStatus("POST", fmt.Sprintf("%s/status?epoch=%d", base, l.Epoch), map[string]any{"incarnation": r.Incarnation, "applied": 1}, nil)
	if st = getStore(c, base); st.Degraded == nil {
		t.Fatal("a healthy report short of the entry cleared the record")
	}
	h2.okStatus("POST", fmt.Sprintf("%s/status?epoch=%d", base, l.Epoch), map[string]any{"incarnation": r.Incarnation, "applied": 2}, nil)
	if st = getStore(c, base); st.Degraded != nil {
		t.Fatalf("the new host replayed the entry; still recorded: %+v", st.Degraded)
	}
}

func getStore(c *apiClient, path string) StoreInfo {
	var st StoreInfo
	c.ok("GET", path, nil, &st)
	return st
}
