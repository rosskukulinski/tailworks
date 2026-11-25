package mgmt

import (
	"context"
	"database/sql"
)

// Per-tenant storage quotas and usage (quota.go), on the stores and queues
// tables. The checks run inside the append's or send's transaction.
//
// SQLite has one writer, so the checks are exact there. On Postgres two
// appends to different stores of a tenant can run at once and each see
// the other's bytes as not yet committed: the tenant can go over its quota
// by at most one in-flight append (4 MiB) or send (256 KB) per store or
// queue. The next write then fails.

// checkTenantStoreBytes: st is the store's row after an append (st.Bytes
// its new size), before its size before. An append that doesn't grow the
// store (an overwrite, a delete) always passes, so a tenant at its quota
// can still free space.
func (s *SQLStore) checkTenantStoreBytes(ctx context.Context, tx *sql.Tx, st StoreInfo, before, tenantMax int64) error {
	if tenantMax <= 0 || st.Bytes <= before {
		return nil
	}
	var others int64
	if err := tx.QueryRowContext(ctx, s.q("SELECT COALESCE(SUM(bytes), 0) FROM stores WHERE tenant_id=? AND NOT (workload=? AND kind=? AND name=?)"),
		st.TenantID, st.Workload, st.Kind, st.Name).Scan(&others); err != nil {
		return err
	}
	// limits.md#tenant-store-bytes
	if others+st.Bytes > tenantMax {
		return &quotaError{Quota: QuotaMaxStoreBytes, Limit: tenantMax, Used: others + before, Requested: st.Bytes - before}
	}
	return nil
}

// checkTenantQueueBytes: a send only ever adds bytes.
func (s *SQLStore) checkTenantQueueBytes(ctx context.Context, tx *sql.Tx, tenantID string, msgs []queueMsgRow, tenantMax int64) error {
	if tenantMax <= 0 {
		return nil
	}
	var add int64
	for _, m := range msgs {
		add += m.Size
	}
	var used int64
	if err := tx.QueryRowContext(ctx, s.q("SELECT COALESCE(SUM(bytes), 0) FROM queues WHERE tenant_id=?"), tenantID).Scan(&used); err != nil {
		return err
	}
	// limits.md#tenant-queue-bytes
	if used+add > tenantMax {
		return &quotaError{Quota: QuotaMaxQueueBytes, Limit: tenantMax, Used: used, Requested: add}
	}
	return nil
}

// TenantStorageUsage sums a tenant's stores and queues, and lists its
// largest (at most n of each, biggest first).
func (s *SQLStore) TenantStorageUsage(ctx context.Context, tenantID string, n int) (TenantUsage, error) {
	u := TenantUsage{TenantID: tenantID, Largest: []UsageItem{}}
	if err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*), COALESCE(SUM(bytes), 0), COALESCE(SUM(entries), 0) FROM stores WHERE tenant_id=?"), tenantID).
		Scan(&u.Stores.Count, &u.Stores.Bytes, &u.Stores.Entries); err != nil {
		return u, err
	}
	if err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*), COALESCE(SUM(bytes), 0), COALESCE(SUM(backlog), 0) FROM queues WHERE tenant_id=?"), tenantID).
		Scan(&u.Queues.Count, &u.Queues.Bytes, &u.Queues.Messages); err != nil {
		return u, err
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT workload, kind, name, bytes FROM stores WHERE tenant_id=? ORDER BY bytes DESC, workload, kind, name LIMIT ?"), tenantID, n)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		it := UsageItem{Limit: StoreMaxBytes}
		if err := rows.Scan(&it.Workload, &it.Kind, &it.Name, &it.Bytes); err != nil {
			rows.Close()
			return u, err
		}
		u.Largest = append(u.Largest, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return u, err
	}
	rows, err = s.db.QueryContext(ctx, s.q("SELECT name, bytes FROM queues WHERE tenant_id=? ORDER BY bytes DESC, name LIMIT ?"), tenantID, n)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		it := UsageItem{Kind: "queue", Limit: QueueMaxBytes}
		if err := rows.Scan(&it.Name, &it.Bytes); err != nil {
			return u, err
		}
		u.Largest = append(u.Largest, it)
	}
	return u, rows.Err()
}
