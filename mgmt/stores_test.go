package mgmt

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const storeValue = "kv-plaintext-0123456789abcdef"

type storeEnv struct {
	e      *testEnv
	tenant string
	epoch  int64
}

func newStoreEnv(t *testing.T) *storeEnv {
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, &dep)
	keepLease(t, e.m, "h1", tn.TenantID, "support", dep.Epoch)
	return &storeEnv{e: e, tenant: tn.TenantID, epoch: dep.Epoch}
}

func (s *storeEnv) journal(epoch int64, q string) string {
	p := "/v1/tenants/" + s.tenant + "/workloads/support/stores/kv/sessions/journal"
	sep := "?"
	if epoch > 0 {
		p += fmt.Sprintf("?epoch=%d", epoch)
		sep = "&"
	}
	if q != "" {
		p += sep + q
	}
	return p
}

func entry(ckey, data string) map[string]any {
	e := map[string]any{"data": []byte(data)}
	if ckey != "" {
		e["ckey"] = ckey
	}
	return e
}

// TestStoresJournal: the lease holder appends conditionally on the head
// (store_conflict otherwise), reads back everything after a seq, a ckey
// supersedes earlier entries (compaction), and the data is sealed at rest.
func TestStoresJournal(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	h1 := s.e.asHost("h1")

	var r StoreAppendResult
	if st := h1.okStatus("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", storeValue+"-a1"), entry("b", "b1")}}, &r); st != 201 {
		t.Fatalf("first append: %d", st)
	}
	if r.Head != 2 || r.Incarnation == "" {
		t.Fatalf("first append: %+v", r)
	}
	inc := r.Incarnation
	// A writer that is behind is refused, whatever it sends.
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 1, "incarnation": inc, "entries": []any{entry("a", "x")}}, 409, "store_conflict")
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", "x")}}, 409, "store_conflict")
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 2, "incarnation": "si_other", "entries": []any{entry("a", "x")}}, 409, "store_conflict")
	// a again (supersedes seq 1), and an entry with no ckey (kept).
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 2, "incarnation": inc, "entries": []any{entry("a", storeValue+"-a2"), entry("", "log")}}, &r)
	if r.Head != 4 {
		t.Fatalf("second append: %+v", r)
	}
	var p StoreJournalPage
	h1.ok("GET", s.journal(s.epoch, ""), nil, &p)
	if p.Head != 4 || p.Incarnation != inc || p.More || len(p.Entries) != 3 {
		t.Fatalf("read: %v", p)
	}
	if p.Entries[0].Seq != 2 || string(p.Entries[0].Data) != "b1" || p.Entries[1].Seq != 3 || string(p.Entries[1].Data) != storeValue+"-a2" || string(p.Entries[2].Data) != "log" {
		t.Fatalf("entries: %+v", p.Entries)
	}
	// after and limit page through the journal.
	h1.ok("GET", s.journal(s.epoch, "after=2&limit=1"), nil, &p)
	if len(p.Entries) != 1 || p.Entries[0].Seq != 3 || !p.More {
		t.Fatalf("page: %v", p)
	}
	var info StoreInfo
	s.e.c.ok("GET", "/v1/tenants/"+s.tenant+"/workloads/support/stores/kv/sessions", nil, &info)
	if info.Head != 4 || info.Entries != 3 || info.Bytes != int64(len("b1")+len(storeValue+"-a2")+len("log")) {
		t.Fatalf("store info: %+v", info)
	}
	if strings.Contains(fmt.Sprint(p), storeValue) {
		t.Fatal("StoreJournalPage prints its data")
	}
	// An admin writes and reads without an epoch (tw kv key put/get); the
	// store object then sees a conflict and catches up.
	s.e.c.ok("POST", s.journal(0, ""), map[string]any{"after": 4, "incarnation": inc, "entries": []any{entry("c", "c1")}}, &r)
	if r.Head != 5 {
		t.Fatalf("admin append: %+v", r)
	}
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 4, "incarnation": inc, "entries": []any{entry("a", "x")}}, 409, "store_conflict")
	// A host must name its epoch.
	directFail(t, s.e, "h1", "GET", s.journal(0, ""), 400, "bad_request")

	// Sealed: no value or key name in plaintext in mgmt's dir, logs or audit.
	assertNoValue(t, s.e, storeValue)

	recs, _ := s.e.store.ListAudit(context.Background(), AuditFilter{TenantID: s.tenant, Kinds: []string{"store_create"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"name":"sessions"`) || !strings.Contains(string(recs[0].Detail), `"by":"h1"`) {
		t.Fatalf("create audit: %+v", recs)
	}
}

// TestStoresFence: only the holder of the workload's lease at its epoch
// reads or writes; after the lease moves, the old holder is refused and the
// new one reads everything.
func TestStoresFence(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	h1, h2 := s.e.asHost("h1"), s.e.asHost("h2")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", "a1")}}, nil)
	h2.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 1, "entries": []any{entry("a", "x")}}, 409, "lease_lost")
	h2.fail("GET", s.journal(s.epoch, ""), nil, 409, "lease_lost")
	h1.fail("GET", s.journal(s.epoch+1, ""), nil, 409, "lease_lost")

	ctx := context.Background()
	l, err := s.e.store.MoveLease(ctx, LeaseName(s.tenant, "support"), "h1", s.epoch, "h2", NamespaceLeaseTTL, s.e.m.now())
	if err != nil {
		t.Fatal(err)
	}
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 1, "entries": []any{entry("a", "stale")}}, 409, "lease_lost")
	var p StoreJournalPage
	h2.ok("GET", s.journal(l.Epoch, ""), nil, &p)
	if p.Head != 1 || len(p.Entries) != 1 || string(p.Entries[0].Data) != "a1" {
		t.Fatalf("new holder reads: %v %+v", p, p.Entries)
	}
	h2.ok("POST", s.journal(l.Epoch, ""), map[string]any{"after": 1, "incarnation": p.Incarnation, "entries": []any{entry("a", "a2")}}, nil)
	recs, _ := s.e.store.ListAudit(ctx, AuditFilter{TenantID: s.tenant, Kinds: []string{"deny"}})
	if len(recs) < 4 {
		t.Fatalf("fence refusals audited: %d", len(recs))
	}
}

// TestStoresLifecycle: create, list, delete (with its data key); a store
// object holding the deleted incarnation gets store_conflict, and the next
// write from 0 starts a new incarnation.
func TestStoresLifecycle(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	c, h1 := s.e.c, s.e.asHost("h1")
	base := "/v1/tenants/" + s.tenant + "/workloads/support/stores"
	var st StoreInfo
	if code := c.okStatus("PUT", base+"/kv/users", nil, &st); code != 201 || st.Incarnation == "" || st.Head != 0 {
		t.Fatalf("create: %d %+v", code, st)
	}
	if code := c.okStatus("PUT", base+"/kv/users", nil, nil); code != 200 {
		t.Fatalf("create again: %d", code)
	}
	var r StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("k", "v")}}, &r)
	var items []StoreInfo
	if next := c.items(base, &items); next != nil || len(items) != 2 || items[0].Name != "sessions" || items[1].Name != "users" {
		t.Fatalf("list: %+v", items)
	}
	var p1 []StoreInfo
	if next := c.items(base+"?limit=1", &p1); next == nil || len(p1) != 1 {
		t.Fatalf("page 1: %+v", p1)
	} else {
		var p2 []StoreInfo
		if n2 := c.items(base+"?limit=1&cursor="+*next, &p2); n2 != nil || len(p2) != 1 || p2[0].Name != "users" {
			t.Fatalf("page 2: %+v", p2)
		}
	}
	c.ok("DELETE", base+"/kv/sessions", nil, nil)
	c.fail("DELETE", base+"/kv/sessions", nil, 404, "unknown_store")
	c.fail("GET", base+"/kv/sessions", nil, 404, "unknown_store")
	if _, err := s.e.m.custody.Get(context.Background(), storeKeyCustodyName(s.tenant, r.Incarnation)); err == nil {
		t.Fatal("the deleted store's data key is still in custody")
	}
	var p StoreJournalPage
	h1.ok("GET", s.journal(s.epoch, ""), nil, &p)
	if p.Incarnation != "" || p.Head != 0 || len(p.Entries) != 0 {
		t.Fatalf("deleted store reads: %v", p)
	}
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 1, "incarnation": r.Incarnation, "entries": []any{entry("k", "v2")}}, 409, "store_conflict")
	var r2 StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("k", "v3")}}, &r2)
	if r2.Incarnation == r.Incarnation || r2.Head != 1 {
		t.Fatalf("new incarnation: %+v (old %s)", r2, r.Incarnation)
	}
	recs, _ := s.e.store.ListAudit(context.Background(), AuditFilter{TenantID: s.tenant, Kinds: []string{"store_delete"}})
	if len(recs) != 1 {
		t.Fatalf("delete audit: %+v", recs)
	}
	c.fail("GET", "/v1/tenants/tn_nope/workloads/support/stores", nil, 404, "unknown_tenant")
}

// TestStoresLimits: entry, append and ckey sizes.
func TestStoresLimits(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	h1 := s.e.asHost("h1")
	big := strings.Repeat("x", StoreEntryMaxBytes+1)
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", big)}}, 400, "store_limit")
	// The spec bounds ckey; the server does too (direct: no validator).
	if st, eb := direct(t, s.e, "h1", "POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry(strings.Repeat("k", StoreCKeyMaxBytes+1), "v")}}); st != 400 || eb["code"] != "store_limit" {
		t.Fatalf("long ckey: %d %v", st, eb)
	}
	half := strings.Repeat("y", StoreEntryMaxBytes)
	h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", half), entry("b", half), entry("c", "z")}}, 400, "store_limit")
	// The largest append (4 MiB, about 5.6 MiB of JSON) is accepted from a
	// host, above the host role's usual 1 MiB body limit.
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", half), entry("b", half)}}, nil)
	var p StoreJournalPage
	h1.ok("GET", s.journal(s.epoch, ""), nil, &p)
	if len(p.Entries) != 2 || len(p.Entries[1].Data) != StoreEntryMaxBytes {
		t.Fatalf("big entries: %d", len(p.Entries))
	}
}
