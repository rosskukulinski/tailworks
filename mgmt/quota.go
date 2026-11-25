package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Per-tenant storage quotas (issue #120). A tenant's platform stores (KV
// namespaces, D1 databases, R2 bucket indexes) and its queues each have a
// size limit of their own (StoreMaxBytes, QueueMaxBytes); these bound the
// tenant as a whole, so one team can't fill the mgmt database for every
// other team:
//
//   - maxStoreBytes: the bytes kept by all its stores' journals together.
//   - maxQueueBytes: the bytes of all its queued messages together.
//   - maxStores: how many stores it may have.
//
// All three default to 0, unlimited: an upgrade never starts refusing an
// existing tenant's writes. The per-store and per-queue limits still
// apply, and `tw doctor` warns as a store, queue or quota fills up.
//
// The byte quotas are checked where data comes in: a journal append that
// grows a store (an overwrite or delete that doesn't grow it always
// passes, so a full tenant can free space) and a queue send. Past the
// quota the write fails 429 storage_quota_exceeded, with
// missing: {quota, limit, used, requested}.

// Quota names (the `missing.quota` of storage_quota_exceeded).
const (
	QuotaMaxStoreBytes = "maxStoreBytes"
	QuotaMaxQueueBytes = "maxQueueBytes"
	QuotaMaxStores     = "maxStores"
)

// UsageLargestN is how many of a tenant's largest stores and queues
// GET /v1/tenants/{id}/usage lists.
const UsageLargestN = 5

// quotaError is a write refused by a tenant quota (from the store layer).
type quotaError struct {
	Quota                  string
	Limit, Used, Requested int64
}

func (e *quotaError) Error() string {
	return fmt.Sprintf("%s %d reached (used %d, this write adds %d)", e.Quota, e.Limit, e.Used, e.Requested)
}

func (e *quotaError) api(tenantID, what string) *Error {
	err := errf(429, "storage_quota_exceeded", "tenant %s: %s would take it past its %s (%d bytes; %d used, the write adds %d)",
		tenantID, what, e.Quota, e.Limit, e.Used, e.Requested)
	if e.Quota == QuotaMaxStores {
		err = errf(429, "storage_quota_exceeded", "tenant %s has %d stores (maxStores %d): %s is not created", tenantID, e.Used, e.Limit, what)
	}
	err.Missing = map[string]any{"quota": e.Quota, "limit": e.Limit, "used": e.Used, "requested": e.Requested}
	return err
}

// asQuotaError converts a store-layer quota refusal to its API error.
func asQuotaError(err error, tenantID, what string) (*Error, bool) {
	var qe *quotaError
	if errors.As(err, &qe) {
		return qe.api(tenantID, what), true
	}
	return nil, false
}

// tenantQuotas reads the tenant's storage quotas (a zero Tenant, no
// quota, when the store has no such tenant: the caller's own checks
// report that).
func (m *Mgmt) tenantQuotas(ctx context.Context, tenantID string) (Tenant, error) {
	t, err := m.store.GetTenant(ctx, tenantID)
	if errors.Is(err, ErrNotFound) {
		return Tenant{}, nil
	}
	return t, err
}

// checkStoreCount refuses a new store past the tenant's maxStores. Two
// concurrent creates can both pass (the count isn't locked): at most one
// store over.
func (m *Mgmt) checkStoreCount(ctx context.Context, tenantID, what string) error {
	t, err := m.tenantQuotas(ctx, tenantID)
	if err != nil || t.MaxStores <= 0 {
		return err
	}
	us, ok := m.store.(usageStore)
	if !ok {
		return nil
	}
	u, err := us.TenantStorageUsage(ctx, tenantID, 0)
	if err != nil {
		return err
	}
	// limits.md#tenant-max-stores
	if u.Stores.Count >= int64(t.MaxStores) {
		e, _ := asQuotaError(&quotaError{Quota: QuotaMaxStores, Limit: int64(t.MaxStores), Used: u.Stores.Count, Requested: 1}, tenantID, what)
		return e
	}
	return nil
}

// usageStore is implemented by stores that can sum a tenant's storage
// (SQLStore; sqlstore_quota.go).
type usageStore interface {
	TenantStorageUsage(ctx context.Context, tenantID string, n int) (TenantUsage, error)
}

// TenantUsage is GET /v1/tenants/{id}/usage: what the tenant keeps in
// mgmt, against its quotas and the per-store and per-queue limits.
type TenantUsage struct {
	TenantID string `json:"tenantID"`
	Stores   struct {
		Count   int64 `json:"count"`
		Bytes   int64 `json:"bytes"`
		Entries int64 `json:"entries"`
	} `json:"stores"`
	Queues struct {
		Count    int64 `json:"count"`
		Bytes    int64 `json:"bytes"`
		Messages int64 `json:"messages"`
	} `json:"queues"`
	Limits UsageLimits `json:"limits"`
	// Largest lists the biggest stores and queues, biggest first.
	Largest []UsageItem `json:"largest"`
}

// UsageLimits are the quotas (0 = unlimited) and the fixed limits.
type UsageLimits struct {
	MaxStoreBytes int64 `json:"maxStoreBytes"`
	MaxQueueBytes int64 `json:"maxQueueBytes"`
	MaxStores     int   `json:"maxStores"`
	StoreMaxBytes int64 `json:"storeMaxBytes"`
	QueueMaxBytes int64 `json:"queueMaxBytes"`
}

// UsageItem is one store (kind kv, d1, r2, …) or queue (kind "queue", no
// workload) and its own size limit.
type UsageItem struct {
	Workload string `json:"workload,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Limit    int64  `json:"limit"`
}

// TenantUsage sums the tenant's stores and queues.
func (m *Mgmt) TenantUsage(ctx context.Context, tenantID string) (TenantUsage, error) {
	t, err := m.store.GetTenant(ctx, tenantID)
	if errors.Is(err, ErrNotFound) {
		return TenantUsage{}, errf(404, "unknown_tenant", "no tenant %s", tenantID)
	}
	if err != nil {
		return TenantUsage{}, err
	}
	us, ok := m.store.(usageStore)
	if !ok {
		return TenantUsage{}, errf(501, "internal", "this mgmt store can't report usage")
	}
	u, err := us.TenantStorageUsage(ctx, tenantID, UsageLargestN)
	if err != nil {
		return TenantUsage{}, err
	}
	sort.SliceStable(u.Largest, func(i, j int) bool { return u.Largest[i].Bytes > u.Largest[j].Bytes })
	if len(u.Largest) > UsageLargestN {
		u.Largest = u.Largest[:UsageLargestN]
	}
	u.Limits = UsageLimits{MaxStoreBytes: t.MaxStoreBytes, MaxQueueBytes: t.MaxQueueBytes, MaxStores: t.MaxStores,
		StoreMaxBytes: StoreMaxBytes, QueueMaxBytes: QueueMaxBytes}
	return u, nil
}

func (a *v1api) GetTenantUsage(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	u, err := a.s.M.TenantUsage(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, u)
}

// ---- PATCH /v1/tenants/{id} ----

// quotaPatch reads one storage quota field of a tenant merge patch into
// p. A number ≥ 0 sets it (0 = unlimited); null clears it (unlimited).
func (p *TenantPatch) quotaPatch(k string, raw json.RawMessage) error {
	var n int64
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &n); err != nil {
			return errf(400, "bad_request", "%s must be an integer", k)
		}
		if n < 0 {
			return errf(400, "bad_request", "%s must be ≥ 0 (0 = unlimited)", k)
		}
	}
	switch k {
	case QuotaMaxStoreBytes:
		p.MaxStoreBytes = &n
	case QuotaMaxQueueBytes:
		p.MaxQueueBytes = &n
	case QuotaMaxStores:
		p.MaxStores = &n
	}
	return nil
}

// applyQuotaPatch sets the patched quotas on t.
func applyQuotaPatch(t *Tenant, p TenantPatch, changed map[string]any) error {
	for _, q := range []struct {
		name string
		p    *int64
		dst  *int64
	}{
		{QuotaMaxStoreBytes, p.MaxStoreBytes, &t.MaxStoreBytes},
		{QuotaMaxQueueBytes, p.MaxQueueBytes, &t.MaxQueueBytes},
	} {
		if q.p == nil {
			continue
		}
		if *q.p < 0 {
			return errf(400, "bad_request", "%s must be ≥ 0 (0 = unlimited)", q.name)
		}
		*q.dst = *q.p
		changed[q.name] = *q.p
	}
	if p.MaxStores != nil {
		if *p.MaxStores < 0 || *p.MaxStores > 1<<31-1 {
			return errf(400, "bad_request", "maxStores must be 0 (unlimited) to %d", 1<<31-1)
		}
		t.MaxStores = int(*p.MaxStores)
		changed[QuotaMaxStores] = t.MaxStores
	}
	return nil
}
