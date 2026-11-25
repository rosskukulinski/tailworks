package mgmt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// Warm-key pool (RFC §4: "keeps warm keys per (tenant, tag set)"). Measured
// on production control (STATUS.md "Fast identity"): CreateKey is 145 ms on
// a good run but 1.5–4.8 s for about 40% of objects on bad ones, all on the
// critical path of a new object's node. The pool pre-mints single-use,
// preauthorized keys per (tenant, canonical tag set, ephemeral) so mintOne
// can skip CreateKey.
//
// Invariants:
//   - A pooled key is taken exactly once (TakeWarmKey: one conditional
//     write) by an object holding the mint claim, and only by an object of
//     the same tenant and the identical PoolName (tags and ephemeral flag).
//     A taken key never returns to the pool, whatever happens next.
//   - The object adopts it through the normal path: custody under its own
//     per-key name, then the fenced status commit with its "mint" audit
//     record; credential release is the unchanged lease-fenced Credential.
//   - Every refill is a mint: it takes a registration token from the shared
//     per-tenant throttle (only when one is free and no object is queued, so
//     real mints keep priority) and its "mint" record (objectID "", detail
//     pool) commits with the pool row.
//   - Expiry and take decisions use the store clock (m.now).
//   - Refills of one pool are done by one replica at a time (a store lease).

// Defaults.
const (
	DefaultWarmKeyTTL = 30 * time.Minute
	// WarmKeyTakeMargin: a key is handed out only with at least this much
	// life left (the host fetches its credential within ~1 s; the release
	// itself needs ≥15 s).
	WarmKeyTakeMargin = 60 * time.Second
	// warmKeySafety is subtracted from the API expiry (clock offset between
	// the store and the Tailscale API).
	warmKeySafety      = 30 * time.Second
	warmDemandWindow   = 10 * time.Minute // create rate sizing window
	warmActiveWindow   = time.Hour        // a pool with no creates/deploys this long is not kept
	warmRefillHorizon  = 2 * time.Minute  // size ≈ creates expected over this long
	defaultWarmTick    = 2 * time.Second
	warmTakenRetention = 10 * time.Minute // taken rows are normally deleted at once; stragglers after this
)

func poolCustodyName(tenantID, keyID string) string {
	return "tw/" + tenantID + "/_pool/authkey/" + keyID // "_pool" is never an object ID (DNS label)
}

func (m *Mgmt) warmStore() warmKeyStore {
	if m.cfg.WarmKeys <= 0 {
		return nil
	}
	ws, _ := m.store.(warmKeyStore)
	return ws
}

// kickPool asks the pool loop to run now (after a take).
func (m *Mgmt) kickPool() {
	select {
	case m.poolKick <- struct{}{}:
	default:
	}
}

func (m *Mgmt) poolLoop() {
	defer m.wg.Done()
	tick := m.cfg.WarmPoolInterval
	for {
		m.poolOnce(m.ctx)
		select {
		case <-m.ctx.Done():
			return
		case <-m.poolKick:
		case <-m.clock.After(tick):
		}
	}
}

// poolOnce expires keys past their take window, sweeps stragglers, and
// refills every active pool toward its target.
func (m *Mgmt) poolOnce(ctx context.Context) {
	ws := m.warmStore()
	if ws == nil {
		return
	}
	m.expireWarm(ctx, ws)
	ts, err := m.store.ListTenants(ctx, Page{Limit: 1000})
	if err != nil {
		return
	}
	for _, t := range ts {
		if m.ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		until := m.poolPause[t.ID]
		m.mu.Unlock()
		if time.Now().Before(until) || m.QueueDepth(t.ID) > 0 || m.isWarmGone(t.ID) {
			continue // backing off, or real mints are queued: they go first
		}
		for _, p := range m.poolTargets(ctx, ws, t) {
			if err := m.refillPool(ctx, ws, t, p); err != nil {
				m.log.Warn("warm-key refill", "tenant", t.ID, "pool", p.name, "err", err)
				m.mu.Lock()
				m.poolPause[t.ID] = time.Now().Add(time.Minute)
				m.mu.Unlock()
				break
			}
		}
	}
}

type poolTarget struct {
	name      string
	tags      []string
	ephemeral bool
	target    int
}

// poolTargets sizes each pool of t from its recent create rate: ≈ the
// creates expected over warmRefillHorizon, at least 1 for a pool with a
// create or a deploy in the last hour, at most cfg.WarmKeys.
func (m *Mgmt) poolTargets(ctx context.Context, ws warmKeyStore, t Tenant) []poolTarget {
	now := m.now()
	byName := map[string]*poolTarget{}
	add := func(tags []string, eph bool, recent int) {
		if len(tags) == 0 {
			return
		}
		name := PoolName(tags, eph)
		p := byName[name]
		if p == nil {
			p = &poolTarget{name: name, tags: canonTags(tags), ephemeral: eph}
			byName[name] = p
		}
		want := (recent*int(warmRefillHorizon/time.Second) + int(warmDemandWindow/time.Second) - 1) / int(warmDemandWindow/time.Second)
		p.target = max(p.target, min(m.cfg.WarmKeys, max(1, want)))
	}
	if active, err := ws.PoolDemand(ctx, t.ID, now.Add(-warmActiveWindow).UnixMilli()); err == nil {
		recent, _ := ws.PoolDemand(ctx, t.ID, now.Add(-warmDemandWindow).UnixMilli())
		n := map[string]int{}
		for _, c := range recent {
			n[PoolName(c.Tags, c.Ephemeral)] += c.N
		}
		for _, c := range active {
			add(c.Tags, c.Ephemeral, n[PoolName(c.Tags, c.Ephemeral)])
		}
	}
	// A fresh deployment's classes get a warm key before their first create.
	if deps, err := m.store.LatestDeployments(ctx, t.ID); err == nil {
		for _, d := range deps {
			if now.Sub(time.UnixMilli(d.CreatedAt)) > warmActiveWindow {
				continue
			}
			for _, c := range d.Namespaces {
				add(c.Tags, false, 0)
			}
		}
	}
	out := make([]poolTarget, 0, len(byName))
	for _, p := range byName {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b poolTarget) int {
		if a.name < b.name {
			return -1
		}
		return 1
	})
	return out
}

// refillPool tops p up toward its target. Only the holder of the pool's
// store lease refills; each new key needs a free throttle token.
func (m *Mgmt) refillPool(ctx context.Context, ws warmKeyStore, t Tenant, p poolTarget) error {
	now := m.now()
	minExp := now.Add(WarmKeyTakeMargin).UnixMilli()
	n, err := ws.CountWarmKeys(ctx, t.ID, p.name, minExp)
	if err != nil || n >= p.target {
		return err
	}
	if _, err := m.store.AcquireLease(ctx, "warmpool/"+t.ID+"/"+p.name, m.replicaID, 3*m.cfg.WarmPoolInterval+5*time.Second, now); err != nil {
		if errors.Is(err, ErrLeaseHeld) {
			return nil // another replica refills this pool
		}
		return err
	}
	th := m.throttle(t)
	for ; n < p.target; n++ {
		if m.QueueDepth(t.ID) > 0 || th.Reserve() != 0 {
			return nil // a real mint is waiting, or no token now: try next tick
		}
		if err := m.mintWarm(ctx, ws, t, p); err != nil {
			var ae *tsapi.APIError
			if errors.As(err, &ae) && (ae.Status == http.StatusTooManyRequests || ae.Status >= 500) {
				th.Backoff(ae.RetryAfter)
			}
			return err
		}
		th.Success()
	}
	return nil
}

// mintWarm mints one pool key: CreateKey, custody, then the pool row with
// its mint record in one transaction.
func (m *Mgmt) mintWarm(ctx context.Context, ws warmKeyStore, t Tenant, p poolTarget) error {
	api, err := m.broker.API(ctx, t, purposeMint)
	if err != nil {
		return err
	}
	ttl := m.cfg.WarmKeyTTL
	t0 := time.Now()
	// Not cancelled by a shutdown: a CreateKey cut off after the API made
	// the key would leave it live there, unknown to mgmt (#158). Close
	// waits for it.
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer ccancel()
	k, err := api.CreateKey(cctx, tsapi.KeyOptions{Tags: p.tags, Ephemeral: p.ephemeral, Preauthorized: true,
		ExpirySeconds: int(ttl / time.Second), Description: "tw pool " + t.ID})
	if err != nil {
		return err
	}
	ck := time.Since(t0)
	// Undo with a context of its own: a shutdown cancels ctx mid-mint, and
	// the key must not be left live at the API (#158).
	undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.custody.Put(ctx, poolCustodyName(t.ID, k.ID), []byte(k.Secret)); err != nil {
		api.DeleteKey(undo, k.ID)
		return fmt.Errorf("custody: %w", err)
	}
	now := m.now()
	wk := WarmKey{KeyID: k.ID, TenantID: t.ID, Pool: p.name, Tags: p.tags, Ephemeral: p.ephemeral, Status: WarmKeyWarm,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(ttl - warmKeySafety).UnixMilli()}
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, Kind: "mint"}, map[string]any{"keyID": k.ID, "tags": p.tags,
		"mode": t.MintMode, "ephemeral": p.ephemeral, "pool": p.name})
	if err := ws.PutWarmKey(actx, wk); err != nil {
		api.DeleteKey(undo, k.ID)
		m.custody.Delete(undo, poolCustodyName(t.ID, k.ID))
		return err
	}
	m.recordIntent(ctx, ai, nil)
	if m.warmRefillLanded(undo, ws, api, wk) {
		return nil
	}
	m.log.Info("warm key minted", "tenant", t.ID, "pool", p.name, "keyID", k.ID, "createkey_ms", msf(ck))
	return nil
}

// adoptWarmKey is mintOne's fast path: take a pooled key for o (the caller
// holds o's mint claim) instead of calling CreateKey. handled=false means
// the pool had nothing usable and the caller mints as before.
func (m *Mgmt) adoptWarmKey(ctx context.Context, t Tenant, o Object, sp *mintSpans) (handled bool, err error) {
	ws := m.warmStore()
	if ws == nil {
		return false, nil
	}
	defer m.kickPool()
	pool := PoolName(o.Tags, o.Ephemeral)
	now := m.now()
	wk, ok, err := ws.TakeWarmKey(ctx, t.ID, pool, o.ID, now.UnixMilli(), now.Add(WarmKeyTakeMargin).UnixMilli())
	if err != nil || !ok {
		return false, nil // the pool is a fast path only: any trouble falls back to a fresh mint
	}
	sp.mark(&sp.createKey)
	// From here the key is this object's or nobody's.
	discard := func(why string, e error) {
		m.log.Warn("warm key discarded", "tenant", t.ID, "keyID", wk.KeyID, "object", o.ID, "why", why, "err", e)
		m.revokeWarm(ctx, t, wk)
	}
	if wk.TenantID != t.ID || wk.Pool != pool || !slices.Equal(canonTags(wk.Tags), canonTags(o.Tags)) || wk.Ephemeral != o.Ephemeral {
		discard("pool mismatch", nil) // defence in depth: the take already filtered on these
		return false, nil
	}
	secret, err := m.custody.Get(ctx, poolCustodyName(t.ID, wk.KeyID))
	if err != nil {
		discard("custody read", err)
		return false, nil
	}
	own := custodyKeyName(o, wk.KeyID)
	if err := m.custody.Put(ctx, own, secret); err != nil {
		discard("custody write", err)
		return true, err // retried by the mint worker (with a fresh take or mint)
	}
	sp.mark(&sp.custody)
	exp := wk.ExpiresAt - (15 * time.Second).Milliseconds() // released only with ≥15 s of life left, as mintOne
	killed := false
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, ObjectID: o.ID, Kind: "mint"}, map[string]any{"keyID": wk.KeyID, "tags": o.Tags,
		"mode": t.MintMode, "ephemeral": o.Ephemeral, "pooled": true, "pool": pool})
	_, err = m.store.UpdateObject(actx, o.ID, nil, func(x *Object) error {
		if x.Status != StatusPending || x.KeyID != "" {
			killed = true
			ai.Skip()
			return nil
		}
		x.Status, x.KeyID, x.KeyExpires, x.UpdatedAt = StatusMinted, wk.KeyID, exp, m.now().UnixMilli()
		return nil
	})
	if err != nil || killed {
		m.custody.Delete(ctx, own)
		discard("object changed", err)
		return true, err
	}
	sp.mark(&sp.commit)
	m.recordIntent(ctx, ai, nil)
	m.custody.Delete(ctx, poolCustodyName(t.ID, wk.KeyID))
	ws.DeleteWarmKey(ctx, wk.TenantID, wk.KeyID)
	m.notify(o.ID)
	m.log.Info("mint spans", append([]any{"object", o.ID, "tenant", t.ID, "keyID", wk.KeyID, "pooled", true}, sp.attrs()...)...)
	sp.observe(t.ID, true)
	return true, nil
}

// revokeWarm revokes a pool key at the API and deletes its custody entry
// and row. Never call it for a key an object has adopted.
func (m *Mgmt) revokeWarm(ctx context.Context, t Tenant, wk WarmKey) {
	api, err := m.broker.API(ctx, t, purposeMint)
	if err == nil {
		err = api.DeleteKey(ctx, wk.KeyID)
	}
	if err != nil {
		m.log.Warn("warm key revoke failed", "tenant", t.ID, "keyID", wk.KeyID, "err", err)
		return // keep the row (and custody): the sweep retries
	}
	m.custody.Delete(ctx, poolCustodyName(t.ID, wk.KeyID))
	if ws := m.warmStore(); ws != nil {
		ws.DeleteWarmKey(ctx, wk.TenantID, wk.KeyID)
	}
}

// expireWarm revokes keys whose take window has closed (store clock), and
// cleans up rows left "taken" by a replica that died mid-adoption.
func (m *Mgmt) expireWarm(ctx context.Context, ws warmKeyStore) {
	now := m.now()
	exp, err := ws.ExpireWarmKeys(ctx, now.Add(WarmKeyTakeMargin).UnixMilli())
	if err != nil {
		m.log.Warn("warm-key expiry", "err", err)
	}
	stale, _ := ws.ListWarmKeys(ctx, WarmKeyTaken)
	var more []WarmKey
	m.mu.Lock()
	retry := time.Since(m.poolRetried) > 30*time.Second
	if retry {
		m.poolRetried = time.Now()
	}
	m.mu.Unlock()
	if retry {
		more, _ = ws.ListWarmKeys(ctx, WarmKeyExpired) // revocations that failed before
	}
	for _, k := range append(exp, more...) {
		t, err := m.store.GetTenant(ctx, k.TenantID)
		switch {
		case err == nil:
			m.revokeWarm(ctx, t, k)
			m.audit(ctx, AuditRecord{TenantID: k.TenantID, Kind: "revoke"}, map[string]any{"keyID": k.KeyID, "pool": k.Pool, "reason": "warm key expired unused"})
		case errors.Is(err, ErrNotFound):
			// The tenant is gone, and with it the credential to revoke: the
			// key expires at the API within WarmKeyTTL. Drop our copy.
			m.custody.Delete(ctx, poolCustodyName(k.TenantID, k.KeyID))
			ws.DeleteWarmKey(ctx, k.TenantID, k.KeyID)
		}
	}
	for _, k := range stale {
		if now.Sub(time.UnixMilli(k.TakenAt)) < warmTakenRetention {
			continue
		}
		t, err := m.store.GetTenant(ctx, k.TenantID)
		if err != nil {
			continue
		}
		if ids, err := ws.ObjectsWithKey(ctx, k.TenantID, k.KeyID); err == nil && len(ids) > 0 {
			// Adopted (the object owns key and custody); only the pool's leftovers go.
			m.custody.Delete(ctx, poolCustodyName(t.ID, k.KeyID))
			ws.DeleteWarmKey(ctx, k.TenantID, k.KeyID)
		} else if err == nil {
			m.revokeWarm(ctx, t, k) // taken but never adopted: nobody may use it
		}
	}
}

// DrainWarmKeys revokes every warm key (all tenants): for tests and
// operators. Keys being adopted are left to their adopters.
func (m *Mgmt) DrainWarmKeys(ctx context.Context) (int, error) { return m.drainWarm(ctx, "") }

// drainWarm revokes the warm keys of tenantID ("" = all tenants).
func (m *Mgmt) drainWarm(ctx context.Context, tenantID string) (int, error) {
	ws, ok := m.store.(warmKeyStore)
	if !ok {
		return 0, nil
	}
	ks, err := ws.ListWarmKeys(ctx, WarmKeyWarm)
	if err != nil {
		return 0, err
	}
	var revoke []tenantKey
	for _, k := range ks {
		if tenantID != "" && k.TenantID != tenantID {
			continue
		}
		// Take it out of the pool atomically first (a concurrent take wins
		// or loses cleanly), then revoke.
		tk, ok, err := ws.TakeWarmKey(ctx, k.TenantID, k.Pool, "_drain", m.now().UnixMilli(), -1<<62)
		if err != nil || !ok {
			continue
		}
		if t, err := m.store.GetTenant(ctx, tk.TenantID); err == nil {
			revoke = append(revoke, tenantKey{t, tk}) // the key the take returned, not necessarily k
		}
	}
	m.revokeWarmAll(ctx, revoke) // warmrevoke.go: in parallel
	return len(revoke), nil
}

// WarmKeyStats is the pool size per (tenant, pool), for status and tests.
func (m *Mgmt) WarmKeyStats(ctx context.Context) (map[string]int, error) {
	ws, ok := m.store.(warmKeyStore)
	if !ok {
		return nil, nil
	}
	ks, err := ws.ListWarmKeys(ctx, WarmKeyWarm)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, k := range ks {
		out[k.TenantID+" "+k.Pool]++
	}
	return out, nil
}
