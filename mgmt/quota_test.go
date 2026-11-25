package mgmt

import (
	"strings"
	"testing"
)

// TestTenantStoreQuota (#120): maxStoreBytes bounds the journals of all
// the tenant's stores together. An append that grows a store past it is
// 429 storage_quota_exceeded with the numbers; one that doesn't grow it
// (an overwrite) passes, so a full tenant can free space; the usage
// endpoint reports it; 0 or null is unlimited again.
func TestTenantStoreQuota(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	c, h1 := s.e.c, s.e.asHost("h1")
	tp := "/v1/tenants/" + s.tenant

	// Unlimited by default: no field, and a big append passes.
	var tn Tenant
	c.ok("GET", tp, nil, &tn)
	if tn.MaxStoreBytes != 0 || tn.MaxQueueBytes != 0 || tn.MaxStores != 0 {
		t.Fatalf("default quotas: %+v", tn)
	}
	var r StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{entry("a", strings.Repeat("x", 60))}}, &r)

	c.ok("PATCH", tp, map[string]any{"maxStoreBytes": 100}, &tn)
	if tn.MaxStoreBytes != 100 {
		t.Fatalf("patched: %+v", tn)
	}

	// A second store (an admin append to another KV namespace) counts too.
	other := strings.Replace(s.journal(0, ""), "/kv/sessions/", "/kv/other/", 1)
	var r2 StoreAppendResult
	c.ok("POST", other, map[string]any{"after": 0, "entries": []any{entry("k", strings.Repeat("y", 30))}}, &r2)

	// 60 + 30 kept; 20 more is over 100.
	body := h1.fail("POST", s.journal(s.epoch, ""), map[string]any{"after": r.Head, "incarnation": r.Incarnation, "entries": []any{entry("b", strings.Repeat("z", 20))}},
		429, "storage_quota_exceeded")
	miss, _ := body["missing"].(map[string]any)
	if miss["quota"] != "maxStoreBytes" || miss["limit"] != float64(100) || miss["used"] != float64(90) || miss["requested"] != float64(20) {
		t.Fatalf("missing: %v", body)
	}
	// Overwriting a with less shrinks the store: allowed at the quota.
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": r.Head, "incarnation": r.Incarnation, "entries": []any{entry("a", strings.Repeat("x", 40))}}, &r)
	// Now 40 + 30: 20 more fits.
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": r.Head, "incarnation": r.Incarnation, "entries": []any{entry("b", strings.Repeat("z", 20))}}, &r)

	var u TenantUsage
	c.ok("GET", tp+"/usage", nil, &u)
	if u.Stores.Count != 2 || u.Stores.Bytes != 90 || u.Stores.Entries != 3 || u.Limits.MaxStoreBytes != 100 || u.Limits.StoreMaxBytes != StoreMaxBytes {
		t.Fatalf("usage: %+v", u)
	}
	if len(u.Largest) != 2 || u.Largest[0].Name != "sessions" || u.Largest[0].Bytes != 60 || u.Largest[0].Kind != "kv" || u.Largest[0].Workload != "support" ||
		u.Largest[1].Name != "other" || u.Largest[1].Limit != StoreMaxBytes {
		t.Fatalf("largest: %+v", u.Largest)
	}
	c.fail("GET", "/v1/tenants/tn_nope/usage", nil, 404, "unknown_tenant")

	// null clears it: unlimited again.
	tn = Tenant{}
	c.ok("PATCH", tp, map[string]any{"maxStoreBytes": nil}, &tn)
	if tn.MaxStoreBytes != 0 {
		t.Fatalf("cleared: %+v", tn)
	}
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": r.Head, "incarnation": r.Incarnation, "entries": []any{entry("c", strings.Repeat("w", 500))}}, &r)
}

// TestTenantMaxStores (#120): maxStores refuses a new store, not writes
// to existing ones.
func TestTenantMaxStores(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	c := s.e.c
	tp := "/v1/tenants/" + s.tenant
	store := func(name string) string { return tp + "/workloads/support/stores/kv/" + name }
	c.ok("PATCH", tp, map[string]any{"maxStores": 2}, nil)
	c.ok("PUT", store("one"), nil, nil)
	c.ok("PUT", store("two"), nil, nil)
	c.ok("PUT", store("two"), nil, nil) // exists: not a new store
	body := c.fail("PUT", store("three"), nil, 429, "storage_quota_exceeded")
	if miss, _ := body["missing"].(map[string]any); miss["quota"] != "maxStores" || miss["used"] != float64(2) {
		t.Fatalf("missing: %v", body)
	}
	// The first append to a new store creates it: refused the same way.
	c.fail("POST", store("four")+"/journal", map[string]any{"after": 0, "entries": []any{entry("k", "v")}}, 429, "storage_quota_exceeded")
	c.ok("POST", store("one")+"/journal", map[string]any{"after": 0, "entries": []any{entry("k", "v")}}, nil)
}

// TestTenantQueueQuota (#120): maxQueueBytes bounds all the tenant's
// queued messages; consuming them frees the space.
func TestTenantQueueQuota(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	c, h1 := q.e.c, q.e.asHost("h1")
	tp := "/v1/tenants/" + q.tenant
	c.ok("PATCH", tp, map[string]any{"maxQueueBytes": 50}, nil)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(strings.Repeat("a", 30), "text")}}, nil)
	h1.ok("POST", q.path("fragile", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(strings.Repeat("b", 15), "text")}}, nil)
	body := h1.fail("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(strings.Repeat("c", 10), "text")}}, 429, "storage_quota_exceeded")
	if miss, _ := body["missing"].(map[string]any); miss["quota"] != "maxQueueBytes" || miss["used"] != float64(45) || miss["requested"] != float64(10) {
		t.Fatalf("missing: %v", body)
	}
	var u TenantUsage
	c.ok("GET", tp+"/usage", nil, &u)
	if u.Queues.Count != 2 || u.Queues.Bytes != 45 || u.Queues.Messages != 2 || u.Limits.MaxQueueBytes != 50 {
		t.Fatalf("usage: %+v", u)
	}
	if len(u.Largest) != 2 || u.Largest[0].Kind != "queue" || u.Largest[0].Name != "jobs" || u.Largest[0].Limit != QueueMaxBytes {
		t.Fatalf("largest: %+v", u.Largest)
	}
	// Consume jobs: its 30 bytes are free again.
	pr := q.pull(h1, "jobs", q.epoch, nil)
	if len(pr.Messages) != 1 {
		t.Fatalf("pull: %+v", pr)
	}
	m := pr.Messages[0]
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": []string{m.LeaseID}}, nil)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(strings.Repeat("c", 30), "text")}}, nil)
}
