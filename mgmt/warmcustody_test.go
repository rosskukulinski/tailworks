package mgmt

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// poolKeyIDs are the keys the pool minted for tenant at the fake API.
func (e *poolEnv) poolKeyIDs() []string {
	e.ts.mu.Lock()
	defer e.ts.mu.Unlock()
	var ids []string
	for id, body := range e.ts.keys {
		if d, _ := body["description"].(string); d == "tw pool "+e.tenant {
			ids = append(ids, id)
		}
	}
	return ids
}

// waitPoolKeys waits until the pool has minted at least n keys for e.tenant.
func (e *poolEnv) waitPoolKeys(t *testing.T, n int) {
	t.Helper()
	for dl := time.Now().Add(5 * time.Second); len(e.poolKeyIDs()) < n; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatalf("pool minted %d keys in 5 s, want ≥ %d", len(e.poolKeyIDs()), n)
		}
	}
}

// requireNoWarmKeysLeft: every key the pool minted was revoked at the API,
// and mgmt holds neither a custody copy nor a row of any of them.
func (e *poolEnv) requireNoWarmKeysLeft(t *testing.T, ids []string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if !e.ts.wasDeleted(id) {
			t.Errorf("warm key %s not revoked at the API", id)
		}
		if _, err := e.m.custody.Get(ctx, poolCustodyName(e.tenant, id)); err == nil {
			t.Errorf("warm key %s still in custody", id)
		}
	}
	ks, err := e.store.ListWarmKeys(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if k.TenantID == e.tenant {
			t.Errorf("warm key row left: %+v", k)
		}
	}
}

// TestWarmKeysCountedInTenant (#158, H25): GET /v1/tenants/{id} reports how
// many pooled preauthorized keys the tenant has in custody (`tw status`).
func TestWarmKeysCountedInTenant(t *testing.T) {
	t.Parallel()
	e := newPoolEnv(t, 0, nil)
	support := PoolName([]string{"tag:tw-agent-support"}, false)
	e.waitWarm(t, support, 1, 5*time.Second)
	for dl := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var tn struct {
			WarmKeys int `json:"warmKeys"`
		}
		e.c.ok("GET", "/v1/tenants/"+e.tenant, nil, &tn)
		ks, _ := e.store.ListWarmKeys(context.Background(), "")
		ks = slices.DeleteFunc(ks, func(k WarmKey) bool { return k.TenantID != e.tenant })
		if tn.WarmKeys >= 1 && tn.WarmKeys == len(ks) { // the pool may refill between the two reads
			return
		}
		if time.Now().After(dl) {
			t.Fatalf("tenant warmKeys = %d, store has %d", tn.WarmKeys, len(ks))
		}
	}
}

// TestWarmKeysRevokedOnClose (#158, H25): a mgmt run with
// RevokeWarmKeysOnClose (tw up, so `tw down`) revokes every unused pooled
// key at the fake Tailscale API when it stops, and leaves none in custody.
// Without it (replicas sharing a store) the pool survives a restart.
func TestWarmKeysRevokedOnClose(t *testing.T) {
	t.Parallel()
	t.Run("tw down", func(t *testing.T) {
		e := newPoolEnv(t, 0, func(c *Config) { c.RevokeWarmKeysOnClose = true })
		e.waitPoolKeys(t, 2)
		e.m.Close()
		ids := e.poolKeyIDs() // after Close: the pool loop has stopped, so this is every key
		if len(ids) < 2 {
			t.Fatalf("pool keys %v", ids)
		}
		e.requireNoWarmKeysLeft(t, ids)
		if !strings.Contains(e.logs.String(), "shutdown: warm keys revoked") {
			t.Error("no shutdown revoke logged")
		}
	})
	t.Run("replica restart", func(t *testing.T) {
		e := newPoolEnv(t, 0, nil)
		e.waitWarm(t, PoolName([]string{"tag:tw-agent-support"}, false), 1, 5*time.Second)
		e.m.Close()
		ks, err := e.store.ListWarmKeys(context.Background(), WarmKeyWarm)
		if err != nil || len(ks) == 0 {
			t.Fatalf("no warm key left after Close: %v", err)
		}
		for _, k := range ks {
			if e.ts.wasDeleted(k.KeyID) {
				t.Fatalf("warm key %s revoked on Close without RevokeWarmKeysOnClose", k.KeyID)
			}
		}
	})
}

// TestWarmKeysRevokedOnTenantDelete (#158, H25): `tw tenant rm` (DELETE
// /v1/tenants/{id}) revokes every unused pooled key at the fake Tailscale
// API and leaves none in custody.
func TestWarmKeysRevokedOnTenantDelete(t *testing.T) {
	t.Parallel()
	e := newPoolEnv(t, 0, nil)
	e.waitPoolKeys(t, 2)
	if st := e.c.okStatus("DELETE", "/v1/tenants/"+e.tenant+"?force=true", nil, nil); st == 202 {
		for dl := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if st, _ := e.c.do("GET", "/v1/tenants/"+e.tenant, nil, nil); st == 404 {
				break
			}
			if time.Now().After(dl) {
				t.Fatal("tenant not deleted in 10 s")
			}
		}
	}
	e.m.Close() // stop the pool loop before reading what it minted
	e.requireNoWarmKeysLeft(t, e.poolKeyIDs())
}
