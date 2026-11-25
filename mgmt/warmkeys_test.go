package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStoreWarmKeyTakeOnce: concurrent takes of one pool hand out each key
// once, only to the right (tenant, pool), never an expired one. SQLite and
// (TW_TEST_POSTGRES_DSN) Postgres.
func TestStoreWarmKeyTakeOnce(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		pa := PoolName([]string{"tag:b", "tag:a"}, false)
		if pa != PoolName([]string{"tag:a", "tag:b", "tag:a"}, false) || pa == PoolName([]string{"tag:a", "tag:b"}, true) {
			t.Fatal("PoolName must canonicalise tags and separate ephemeral")
		}
		pb := PoolName([]string{"tag:a"}, false)
		run := fmt.Sprintf("%d", time.Now().UnixNano()) // tenants unique per run: the Postgres database is shared
		put := func(id, tenant, pool string, exp int64) {
			tenant += run
			if err := s.PutWarmKey(ctx, WarmKey{KeyID: id, TenantID: tenant, Pool: pool, Tags: []string{"tag:a"}, CreatedAt: 1, ExpiresAt: exp}); err != nil {
				t.Fatal(err)
			}
		}
		put("k1", "A", pa, 10_000)
		put("k2", "A", pa, 20_000)
		put("k3", "A", pa, 30_000)
		put("old", "A", pa, 1_000) // past the take window
		put("other-pool", "A", pb, 30_000)
		put("other-tenant", "B", pa, 30_000)
		var mu sync.Mutex
		got := map[string]string{}
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				obj := fmt.Sprintf("o%d", i)
				k, ok, err := s.TakeWarmKey(ctx, "A"+run, pa, obj, 500, 5_000)
				if err != nil {
					t.Error(err)
					return
				}
				if ok {
					mu.Lock()
					if prev, dup := got[k.KeyID]; dup {
						t.Errorf("key %s taken twice (%s, %s)", k.KeyID, prev, obj)
					}
					got[k.KeyID] = obj
					mu.Unlock()
					if k.TakenBy != obj || k.Status != WarmKeyTaken {
						t.Errorf("take returned %+v", k)
					}
				}
			}()
		}
		wg.Wait()
		if len(got) != 3 || got["k1"] == "" || got["k2"] == "" || got["k3"] == "" {
			t.Fatalf("taken %v, want exactly k1..k3", got)
		}
		if n, _ := s.CountWarmKeys(ctx, "A"+run, pa, 5_000); n != 0 {
			t.Fatalf("%d warm keys left in pool A/pa, want 0 (\"old\" is outside the window)", n)
		}
		exp, err := s.ExpireWarmKeys(ctx, 5_000)
		exp = slices.DeleteFunc(exp, func(k WarmKey) bool { return !strings.HasSuffix(k.TenantID, run) })
		if err != nil || len(exp) != 1 || exp[0].KeyID != "old" {
			t.Fatalf("expire: %+v %v", exp, err)
		}
		if _, ok, _ := s.TakeWarmKey(ctx, "A"+run, pa, "late", 500, 0); ok {
			t.Fatal("an expired or taken key was handed out")
		}
		for _, id := range []string{"other-pool", "other-tenant"} {
			ks, _ := s.ListWarmKeys(ctx, WarmKeyWarm)
			if !slices.ContainsFunc(ks, func(k WarmKey) bool { return k.KeyID == id && strings.HasSuffix(k.TenantID, run) }) {
				t.Fatalf("%s was taken by the wrong pool or tenant", id)
			}
		}
	})
}

// offsetClock is the store clock jumped forward by off.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) Now() time.Time { return time.Now().Add(time.Duration(c.off.Load())) }

type poolEnv struct {
	*testEnv
	h1     *apiClient
	tenant string
}

func newPoolEnv(t *testing.T, rpm int, mutate func(*Config)) *poolEnv {
	e := newTestEnvWith(t, func(c *Config) {
		c.WarmKeys, c.WarmPoolInterval = 3, 50*time.Millisecond
		if mutate != nil {
			mutate(c)
		}
	})
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	body := map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}
	if rpm > 0 {
		body["registrationsPerMinute"] = rpm
	}
	c.ok("POST", "/v1/tenants", body, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, nil)
	return &poolEnv{testEnv: e, h1: h1, tenant: tn.TenantID}
}

func (e *poolEnv) warm(pool string) int {
	st, _ := e.m.WarmKeyStats(context.Background())
	return st[e.tenant+" "+pool]
}

func (e *poolEnv) waitWarm(t *testing.T, pool string, n int, within time.Duration) {
	t.Helper()
	for dl := time.Now().Add(within); e.warm(pool) < n; {
		if time.Now().After(dl) {
			l := e.logs.String()
			t.Fatalf("pool %s has %d warm keys after %v, want ≥ %d\nlogs (tail):\n%s", pool, e.warm(pool), within, n, l[max(0, len(l)-3000):])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// keyDesc is the description a key was created with at the fake API.
func (f *fakeTS) keyDesc(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, _ := f.keys[id]["description"].(string)
	return d
}

func (f *fakeTS) keyTags(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	if tags, _ := getPath(f.keys[id], "capabilities", "devices", "create", "tags").([]any); tags != nil {
		for _, tg := range tags {
			out = append(out, fmt.Sprint(tg))
		}
	}
	return out
}

func (f *fakeTS) keyEphemeral(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, _ := getPath(f.keys[id], "capabilities", "devices", "create", "ephemeral").(bool)
	return v
}

func (f *fakeTS) wasDeleted(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.deletedKeys, id)
}

// TestWarmKeyPool: a deploy seeds a pool per class; a create adopts a
// pooled key (no CreateKey for it), carrying exactly the object's tags; the
// credential is released through the normal fenced path; mint and release
// are audited; the pool refills; a kill revokes the adopted key.
func TestWarmKeyPool(t *testing.T) {
	t.Parallel()
	e := newPoolEnv(t, 0, nil)
	support := PoolName([]string{"tag:tw-agent-support"}, false)
	rogue := PoolName([]string{"tag:crm"}, false)
	e.waitWarm(t, support, 1, 5*time.Second)
	e.waitWarm(t, rogue, 1, 5*time.Second)

	var o CreateObjectResponse
	e.h1.ok("POST", "/v1/objects", map[string]any{"tenantID": e.tenant, "namespace": "SupportAgent", "objectName": "alice"}, &o)
	t0 := time.Now()
	var cred Credential
	e.h1.ok("POST", "/v1/objects/"+o.ObjectID+"/credentials?epoch=1&wait=10", nil, &cred)
	t.Logf("create → credential with a warm key: %v", time.Since(t0).Round(time.Millisecond))
	obj, _ := e.store.GetObject(context.Background(), o.ObjectID) // the v1 view hides keyID
	if !strings.HasPrefix(e.ts.keyDesc(obj.KeyID), "tw pool ") {
		t.Fatalf("object key %s was not a pooled key (description %q)", obj.KeyID, e.ts.keyDesc(obj.KeyID))
	}
	e.ts.mu.Lock()
	secret := e.ts.secrets[obj.KeyID]
	for id, b := range e.ts.keys {
		if b["description"] == "tw "+o.ObjectID {
			t.Errorf("a fresh key %s was minted for the object despite a warm one", id)
		}
	}
	e.ts.mu.Unlock()
	if cred.AuthKey != secret {
		t.Fatal("the released credential is not the adopted key")
	}
	if got := e.ts.keyTags(obj.KeyID); !slices.Equal(got, []string{"tag:tw-agent-support"}) || e.ts.keyEphemeral(obj.KeyID) {
		t.Fatalf("adopted key tags %v ephemeral %v, want exactly the object's", got, e.ts.keyEphemeral(obj.KeyID))
	}
	if _, err := e.m.custody.Get(context.Background(), poolCustodyName(e.tenant, obj.KeyID)); err == nil {
		t.Error("the pool's custody copy of an adopted key was not deleted")
	}
	// Single use: the stale fence and a second release are refused as before.
	e.asHost("h2").fail("POST", "/v1/objects/"+o.ObjectID+"/credentials?epoch=1", nil, 409, "lease_lost")

	var aud struct {
		Items []AuditRecord `json:"items"`
	}
	e.c.ok("GET", "/v1/audit?object="+o.ObjectID, nil, &aud)
	var kinds []string
	pooled := false
	for _, r := range aud.Items {
		kinds = append(kinds, r.Kind)
		var d map[string]any
		json.Unmarshal(r.Detail, &d)
		if r.Kind == "mint" && d["pooled"] == true && d["keyID"] == obj.KeyID {
			pooled = true
		}
	}
	if !pooled || !slices.Contains(kinds, "credential") || !slices.Contains(kinds, "create") {
		t.Fatalf("object audit %v (pooled mint record %v)", kinds, pooled)
	}
	// The refill that produced the key is audited too (a tenant-level mint).
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{Chain: "mgmt"})
	refill := false
	for _, r := range recs {
		var d map[string]any
		json.Unmarshal(r.Detail, &d)
		if r.Kind == "mint" && r.ObjectID == "" && d["keyID"] == obj.KeyID && d["pool"] == support {
			refill = true
		}
	}
	if !refill {
		t.Fatal("no audit record for the refill mint of the adopted key")
	}
	e.waitWarm(t, support, 1, 5*time.Second) // refilled after the take

	// A different class never gets this pool's keys.
	var r CreateObjectResponse
	e.h1.ok("POST", "/v1/objects", map[string]any{"tenantID": e.tenant, "namespace": "Rogue", "objectName": "r1"}, &r)
	e.h1.ok("POST", "/v1/objects/"+r.ObjectID+"/credentials?epoch=1&wait=10", nil, &cred)
	ro, _ := e.store.GetObject(context.Background(), r.ObjectID)
	if got := e.ts.keyTags(ro.KeyID); !slices.Equal(got, []string{"tag:crm"}) {
		t.Fatalf("Rogue object got a key tagged %v", got)
	}
	// Kill before the join: the adopted key is revoked like any minted key.
	e.c.ok("DELETE", "/v1/objects/"+o.ObjectID+killQuery(false, "test"), nil, nil)
	for dl := time.Now().Add(5 * time.Second); !e.ts.wasDeleted(obj.KeyID); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatal("killing the object did not revoke its adopted key")
		}
	}
	if strings.Contains(e.logs.String(), "tskey-") {
		t.Fatal("a key reached the logs")
	}
}

// TestWarmKeyExpiry: unused pooled keys are revoked before their API expiry
// (decided on the store clock) and replaced; their custody copies go too.
func TestWarmKeyExpiry(t *testing.T) {
	t.Parallel()
	clk := &offsetClock{}
	e := newPoolEnv(t, 0, func(c *Config) {
		c.WarmKeyTTL = 3 * time.Minute
		c.Store.(*SQLStore).SetClock(clk)
	})
	support := PoolName([]string{"tag:tw-agent-support"}, false)
	e.waitWarm(t, support, 1, 5*time.Second)
	ws := e.m.warmStore()
	before, _ := ws.ListWarmKeys(context.Background(), WarmKeyWarm)
	before = slices.DeleteFunc(before, func(k WarmKey) bool { return k.TenantID != e.tenant }) // shared test DB
	// Inside the take window: nothing is revoked.
	clk.off.Store(int64(time.Minute))
	time.Sleep(300 * time.Millisecond)
	for _, k := range before {
		if e.ts.wasDeleted(k.KeyID) {
			t.Fatalf("key %s revoked while still takeable", k.KeyID)
		}
	}
	// Past the take window (TTL − 30 s safety − 60 s margin = 90 s).
	clk.off.Store(int64(100 * time.Second))
	for dl := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		all := true
		for _, k := range before {
			all = all && e.ts.wasDeleted(k.KeyID)
		}
		if all {
			break
		}
		if time.Now().After(dl) {
			t.Fatal("expired warm keys were not revoked")
		}
	}
	// revokeWarm deletes the custody copy after DeleteKey returns and the row
	// last, so the API seeing the delete doesn't mean custody is clean yet
	// (#248). Wait for the rows to go, then custody must be clean.
	rowsLeft := func() []string {
		ks, _ := ws.ListWarmKeys(context.Background(), "")
		have := map[string]bool{}
		for _, k := range ks {
			have[k.KeyID] = true
		}
		var left []string
		for _, k := range before {
			if have[k.KeyID] {
				left = append(left, k.KeyID)
			}
		}
		return left
	}
	for dl := time.Now().Add(5 * time.Second); len(rowsLeft()) > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatalf("rows of revoked keys %v kept", rowsLeft())
		}
	}
	for _, k := range before {
		if _, err := e.m.custody.Get(context.Background(), poolCustodyName(e.tenant, k.KeyID)); err == nil {
			t.Errorf("custody copy of revoked key %s kept", k.KeyID)
		}
	}
	// expireWarm writes each key's "revoke" record after its DeleteKey
	// returns, so seeing the delete at the API doesn't mean the record is
	// there yet. Nor does a replacement key: the refill doesn't count keys
	// inside the take margin, so it can mint one before they expire (#137).
	// Wait for the records themselves.
	unaudited := func() []string {
		recs, _ := e.store.ListAudit(context.Background(), AuditFilter{Chain: "mgmt", TenantID: e.tenant, Kinds: []string{"revoke"}})
		got := map[string]bool{}
		for _, r := range recs {
			var d struct{ KeyID string }
			json.Unmarshal(r.Detail, &d)
			got[d.KeyID] = true
		}
		var missing []string
		for _, k := range before {
			if !got[k.KeyID] {
				missing = append(missing, k.KeyID)
			}
		}
		return missing
	}
	for dl := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		missing := unaudited()
		if len(missing) == 0 {
			break
		}
		if time.Now().After(dl) {
			t.Fatalf("no revoke record for %d of %d expired keys: %v", len(missing), len(before), missing)
		}
	}
	e.waitWarm(t, support, 1, 5*time.Second) // replaced with fresh keys
	// Drain revokes the keys that are warm when it runs. The pool loop keeps
	// running, and the deployment is under an hour old, so it may refill
	// right after (#137): check the drained keys, not an empty pool.
	warm := func() map[string]bool {
		ks, _ := ws.ListWarmKeys(context.Background(), WarmKeyWarm)
		out := map[string]bool{}
		for _, k := range ks {
			if k.TenantID == e.tenant {
				out[k.KeyID] = true
			}
		}
		return out
	}
	pre := warm()
	if n, err := e.m.DrainWarmKeys(context.Background()); err != nil || n == 0 {
		t.Fatalf("drain: %d %v", n, err)
	}
	post := warm()
	for id := range pre {
		if post[id] {
			t.Fatalf("warm key %s left after drain (warm before: %v, after: %v)", id, pre, post)
		}
		if !e.ts.wasDeleted(id) {
			t.Fatalf("drained warm key %s not revoked at the API", id)
		}
	}
}

// TestWarmKeyRefillRespectsThrottle: refills spend the tenant's shared
// registration budget (ADR-017) and never get ahead of it. At 10/min
// (burst 1), the deploy's seed refill takes the one token; after a take,
// the next refill waits for a token (~6 s), not a moment less.
func TestWarmKeyRefillRespectsThrottle(t *testing.T) {
	t.Parallel()
	e := newPoolEnv(t, 10, nil)
	classOf := map[string]string{
		PoolName([]string{"tag:tw-agent-support"}, false):     "SupportAgent",
		PoolName([]string{"tag:tw-agent-support-sub"}, false): "LookupAgent",
		PoolName([]string{"tag:crm"}, false):                  "Rogue",
	}
	// seeded: when the seed key was first seen. Its CreateKey spent the one
	// token just before, so the next token is due ~6 s after it (#137: timing
	// from the take instead failed under load, when the take came > 2 s later).
	var pool string
	var seeded time.Time
	for dl := time.Now().Add(5 * time.Second); pool == ""; time.Sleep(20 * time.Millisecond) {
		st, _ := e.m.WarmKeyStats(context.Background())
		for k := range st {
			if p, ok := strings.CutPrefix(k, e.tenant+" "); ok { // a shared test database holds other tests' pools too
				pool = p
			}
		}
		seeded = time.Now()
		if time.Now().After(dl) {
			t.Fatal("no warm key after 5 s")
		}
	}
	time.Sleep(time.Second) // three pools want a key; the burst allows one
	count := func() int {
		e.ts.mu.Lock()
		defer e.ts.mu.Unlock()
		return len(e.ts.keys)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d keys minted with a burst of 1, want 1", n)
	}
	var o CreateObjectResponse
	e.h1.ok("POST", "/v1/objects", map[string]any{"tenantID": e.tenant, "namespace": classOf[pool], "objectName": "alice"}, &o)
	var cred Credential
	t0 := time.Now()
	e.h1.ok("POST", "/v1/objects/"+o.ObjectID+"/credentials?epoch=1&wait=10", nil, &cred)
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("the warm key was not used: credential after %v", d)
	}
	time.Sleep(2 * time.Second)
	if n := count(); n != 1 {
		t.Fatalf("%d keys minted within ~3 s at 10/min (burst 1)", n)
	}
	// One token per 6 s: the next key comes, but only then.
	for dl := time.Now().Add(10 * time.Second); count() < 2; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatal("no refill within 10 s at 10/min")
		}
	}
	if d := time.Since(seeded); d < 4*time.Second {
		t.Fatalf("refill %v after the seed mint, faster than the 10/min budget allows", d)
	}
}
