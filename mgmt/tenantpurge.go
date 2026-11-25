package mgmt

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Tenant purge (issue #107): `tw tenant rm` removes everything the tenant
// owns, not just the tenant row.
//
//  1. Its custody entries are deleted first: every store's and queue's data
//     key (the crypto-shred DeleteStore and DeleteQueue promise), every
//     workload's secrets and volume key, its objects' and warm pool's auth
//     keys, its notification and telemetry settings, its OBO roots, and
//     every other name indexed under it (custody_names).
//  2. Then one transaction deletes every row it owns (KV/D1/R2 journals,
//     queue messages, objects, deployments with their bundles, leases,
//     warm keys, throttle, measured paths, …) and the tenant row, and
//     queues a tenant_purges row per host.
//  3. Each host removes tenants/<id>/ (node state, DO storage, logs) and
//     the tenant's R2 blobs when its heartbeat reply lists the tenant
//     (purgeTenants), and reports it done (purgedTenants); mgmt audits
//     tenant_purge_host and stops listing it.
//
// Every step is idempotent and the tenant row goes last, so a mgmt that
// dies part way leaves a tenant `tw tenant rm` can run again to finish.

// purgeTenant runs steps 1 and 2 and returns what it removed.
func (m *Mgmt) purgeTenant(ctx context.Context, id string) (map[string]int64, error) {
	ps, ok := m.store.(tenantPurgeStore)
	if !ok {
		return nil, m.store.DeleteTenant(ctx, id)
	}
	undoGone := m.markWarmGone(id) // no refill from here on (one in flight revokes its own key)
	done := false
	defer func() {
		if !done {
			undoGone() // the delete failed: the tenant keeps its pool
		}
	}()
	if _, err := m.drainWarm(ctx, id); err != nil { // revoke unused pool keys while the credential is here
		m.log.Warn("tenant delete: warm keys not revoked; they expire at the API", "tenant", id, "err", err)
	}
	refs, err := ps.TenantCustodyRefs(ctx, id)
	if err != nil {
		return nil, err
	}
	names := m.tenantCustodyNames(ctx, id, refs)
	if err := m.deleteCustodyNames(ctx, names); err != nil {
		return nil, fmt.Errorf("tenant %s: custody: %w (run the delete again to finish it)", id, err)
	}
	counts, indexed, err := ps.PurgeTenant(ctx, id, m.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	done = true // the tenant is gone: its mark stays, for refills still in flight
	// Names indexed after the read above (a racing write) go too.
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	var late []string
	for _, n := range indexed {
		if !seen[n] {
			late = append(late, n)
		}
	}
	if err := m.deleteCustodyNames(ctx, late); err != nil {
		m.log.Warn("tenant deleted, but custody entries written during the delete remain", "tenant", id, "n", len(late), "err", err)
	}
	counts["custodyEntries"] = int64(len(names) + len(late))
	for _, inc := range refs.StoreIncarnations {
		storeKeys.Delete(inc)
	}
	for _, inc := range refs.QueueIncarnations {
		queueKeys.Delete(inc)
	}
	return counts, nil
}

// tenantCustodyNames lists every custody name the tenant can have an
// entry under (absent names are fine: Delete ignores them).
func (m *Mgmt) tenantCustodyNames(ctx context.Context, id string, refs tenantCustodyRefs) []string {
	return tenantCustodyCandidates(ctx, m.store, id, refs)
}

// tenantCustodyCandidates is tenantCustodyNames without a Mgmt (the custody
// migration, custodymigrate.go, derives names from the store alone).
func tenantCustodyCandidates(ctx context.Context, sc secretChooser, id string, refs tenantCustodyRefs) []string {
	set := map[string]bool{
		notifyCustodyName(id):    true,
		oboCustodyName(id):       true,
		telemetryCustodyName(id): true,
	}
	if kid, err := sc.SecretChoice(ctx, oboChoiceName(id)); err == nil {
		set[oboKeyName(id, kid)] = true
	}
	for _, inc := range refs.StoreIncarnations {
		set[storeKeyCustodyName(id, inc)] = true
	}
	for _, inc := range refs.QueueIncarnations {
		set[queueKeyCustodyName(id, inc)] = true
	}
	for _, w := range refs.Workloads {
		set[secretsCustodyName(id, w)] = true
		set[volumeKeyName(id, w)] = true
	}
	for _, o := range refs.Objects {
		set[custodyName(o)] = true
		set[legacyCustodyName(o)] = true
	}
	for _, k := range refs.WarmKeyIDs {
		set[poolCustodyName(id, k)] = true
	}
	for _, n := range refs.Indexed {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m *Mgmt) deleteCustodyNames(ctx context.Context, names []string) error {
	for _, n := range names {
		if err := m.custody.Delete(ctx, n); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// tenantPurgesFor is the heartbeat's part (step 3): it records the purges
// hostID reports done (audited tenant_purge_host) and returns the ones
// still pending for it.
func (m *Mgmt) tenantPurgesFor(ctx context.Context, hostID string, done []string) []string {
	ps, ok := m.store.(tenantPurgeStore)
	if !ok {
		return nil
	}
	for _, id := range done {
		if ok, err := ps.AckTenantPurge(ctx, hostID, id); err == nil && ok {
			m.audit(ctx, AuditRecord{TenantID: id, Kind: "tenant_purge_host"}, map[string]any{"host": hostID})
		}
	}
	ids, err := ps.TenantPurges(ctx, hostID)
	if err != nil {
		m.log.Warn("tenant purges", "host", hostID, "err", err)
		return nil
	}
	return ids
}

// indexedCustody records every tenant custody name it writes in the store
// (custody_names), so a tenant's entries can be listed when it is deleted.
// The name is indexed before the value is written, so no entry exists that
// the index does not know.
type indexedCustody struct {
	Custody
	idx tenantPurgeStore
	now func() int64
}

// indexCustody wraps c when the store can index names.
func indexCustody(c Custody, st Store, now func() int64) Custody {
	ps, ok := st.(tenantPurgeStore)
	if !ok || c == nil {
		return c
	}
	return &indexedCustody{Custody: c, idx: ps, now: now}
}

// custodyTenant is the tenant of a custody name ("tw/<tenant>/…"), or "".
func custodyTenant(name string) string {
	rest, ok := strings.CutPrefix(name, "tw/")
	if !ok {
		return ""
	}
	t, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return t
}

func (c *indexedCustody) Put(ctx context.Context, name string, value []byte) error {
	if t := custodyTenant(name); t != "" {
		if err := c.idx.IndexCustodyName(ctx, name, t, c.now()); err != nil {
			return fmt.Errorf("custody index: %w", err)
		}
	}
	return c.Custody.Put(ctx, name, value)
}

func (c *indexedCustody) Delete(ctx context.Context, name string) error {
	if err := c.Custody.Delete(ctx, name); err != nil {
		return err
	}
	if custodyTenant(name) != "" {
		c.idx.UnindexCustodyName(ctx, name) // a stale index row only costs a no-op delete later
	}
	return nil
}
