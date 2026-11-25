package mgmt

import (
	"context"
	"time"
)

// Warm keys in custody (issue #158, ledger H25). A pooled key is a
// preauthorized join key: anyone who reads mgmt's custody can join a node
// with it until it expires (WarmKeyTTL). So the count is visible (GET
// /v1/tenants/{id} `warmKeys`, shown by `tw status`), and a mgmt that stops
// with Config.RevokeWarmKeysOnClose (what `tw up` runs, so `tw down`)
// revokes them at the API and deletes their custody entries, instead of
// leaving them there until they expire. `tw tenant rm` revokes the tenant's
// keys before its custody is shredded (purgeTenant).

// warmKeysHeld counts tenantID's pooled keys whose custody entry mgmt may
// still hold: warm ones, expired ones whose revocation hasn't succeeded yet,
// and ones being adopted right now.
func (m *Mgmt) warmKeysHeld(ctx context.Context, tenantID string) int {
	ws, ok := m.store.(warmKeyStore)
	if !ok {
		return 0
	}
	ks, err := ws.ListWarmKeys(ctx, "")
	if err != nil {
		return 0
	}
	n := 0
	for _, k := range ks {
		if k.TenantID == tenantID {
			n++
		}
	}
	return n
}

// revokeWarmOnClose revokes every unused pooled key, and retries the
// revocations that failed before. Close calls it after the pool loop has
// stopped, so nothing refills behind it.
func (m *Mgmt) revokeWarmOnClose() {
	ws, ok := m.store.(warmKeyStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := m.drainWarm(ctx, "")
	if err != nil {
		m.log.Warn("shutdown: warm keys not revoked; they expire at the API", "err", err)
	}
	failed, _ := ws.ListWarmKeys(ctx, WarmKeyExpired)
	var retry []tenantKey
	for _, k := range failed {
		if t, err := m.store.GetTenant(ctx, k.TenantID); err == nil {
			retry = append(retry, tenantKey{t, k})
		}
	}
	m.revokeWarmAll(ctx, retry)
	n += len(retry)
	if n > 0 {
		m.audit(ctx, AuditRecord{Kind: "revoke"}, map[string]any{"warmKeys": n, "reason": "mgmt stopped: warm keys revoked"})
		m.log.Info("shutdown: warm keys revoked", "n", n)
	}
}

// markWarmGone stops tenantID's pool for good before a tenant delete drains
// it: a refill already past its tenant check sees the mark after its row
// commits and revokes its own key (warmRefillLanded), so no key minted
// during the delete stays live at the API. undo clears the mark when the
// delete fails.
func (m *Mgmt) markWarmGone(tenantID string) (undo func()) {
	m.mu.Lock()
	if m.warmGone == nil {
		m.warmGone = map[string]bool{}
	}
	m.warmGone[tenantID] = true
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.warmGone, tenantID)
		m.mu.Unlock()
	}
}

func (m *Mgmt) isWarmGone(tenantID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.warmGone[tenantID]
}

// warmRefillLanded runs after a refill committed wk's row: if the tenant's
// delete has begun, its drain may have missed wk, so revoke it here with
// the API client the mint used (the tenant row may be gone already).
func (m *Mgmt) warmRefillLanded(ctx context.Context, ws warmKeyStore, api TailnetAPI, wk WarmKey) bool {
	if !m.isWarmGone(wk.TenantID) {
		return false
	}
	if err := api.DeleteKey(ctx, wk.KeyID); err != nil {
		m.log.Warn("warm key minted during a tenant delete not revoked; it expires at the API", "tenant", wk.TenantID, "keyID", wk.KeyID, "err", err)
	}
	m.custody.Delete(ctx, poolCustodyName(wk.TenantID, wk.KeyID))
	ws.DeleteWarmKey(ctx, wk.TenantID, wk.KeyID)
	return true
}
