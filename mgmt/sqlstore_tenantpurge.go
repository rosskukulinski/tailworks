package mgmt

import (
	"context"
	"database/sql"
	"strings"
)

// ---- tenant purge (issue #107) ----
//
// Deleting a tenant removes every row it owns in one transaction
// (PurgeTenant), queues the removal of its local state on every host
// (tenant_purges, delivered in the heartbeat reply), and returns what it
// removed for the tenant_delete audit record. The audit chain is the one
// thing kept: it is append-only and is the record of the deletion.

// tenantCustodyRefs is what names a tenant's custody entries: custody
// cannot list names, so the purge derives them from these rows, plus every
// name mgmt indexed when it wrote it (custody_names).
type tenantCustodyRefs struct {
	StoreIncarnations []string
	QueueIncarnations []string
	Workloads         []string
	Objects           []Object // ID and KeyID only
	WarmKeyIDs        []string
	Indexed           []string
}

// tenantPurgeStore is implemented by SQLStore.
type tenantPurgeStore interface {
	TenantCustodyRefs(ctx context.Context, tenantID string) (tenantCustodyRefs, error)
	// PurgeTenant deletes every row of the tenant and the tenant row
	// (ErrNotFound if it was already gone), queues a tenant_purges row for
	// every registered host, and returns the row counts removed and the
	// custody names that were indexed for the tenant.
	PurgeTenant(ctx context.Context, tenantID string, now int64) (map[string]int64, []string, error)
	// TenantPurges lists the deleted tenants hostID has not purged yet.
	TenantPurges(ctx context.Context, hostID string) ([]string, error)
	// AckTenantPurge records that hostID purged tenantID (ok: it was pending).
	AckTenantPurge(ctx context.Context, hostID, tenantID string) (bool, error)
	// IndexCustodyName / UnindexCustodyName keep custody_names.
	IndexCustodyName(ctx context.Context, name, tenantID string, now int64) error
	UnindexCustodyName(ctx context.Context, name string) error
}

func (s *SQLStore) TenantCustodyRefs(ctx context.Context, id string) (tenantCustodyRefs, error) {
	var r tenantCustodyRefs
	lists := []struct {
		q   string
		out *[]string
	}{
		{"SELECT incarnation FROM stores WHERE tenant_id=? ORDER BY incarnation", &r.StoreIncarnations},
		{"SELECT incarnation FROM queues WHERE tenant_id=? ORDER BY incarnation", &r.QueueIncarnations},
		{`SELECT workload FROM deployments WHERE tenant_id=?
			UNION SELECT workload FROM stores WHERE tenant_id=?
			UNION SELECT workload FROM objects WHERE tenant_id=? AND workload<>''
			UNION SELECT workload FROM workload_public WHERE tenant_id=?
			UNION SELECT workload FROM container_reservations WHERE tenant_id=?
			ORDER BY 1`, &r.Workloads},
		{"SELECT key_id FROM warm_keys WHERE tenant_id=? ORDER BY key_id", &r.WarmKeyIDs},
		{"SELECT name FROM custody_names WHERE tenant_id=? ORDER BY name", &r.Indexed},
	}
	for _, l := range lists {
		args := make([]any, strings.Count(l.q, "?"))
		for i := range args {
			args[i] = id
		}
		vs, err := s.column(ctx, s.db, l.q, args...)
		if err != nil {
			return r, err
		}
		*l.out = vs
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT id, key_id FROM objects WHERE tenant_id=? ORDER BY id"), id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		o := Object{TenantID: id}
		if err := rows.Scan(&o.ID, &o.KeyID); err != nil {
			return r, err
		}
		r.Objects = append(r.Objects, o)
	}
	return r, rows.Err()
}

type columnQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// column runs a one-column query.
func (s *SQLStore) column(ctx context.Context, db columnQuerier, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// tenantPurgeDeletes are the tenant's rows, in order (children before what
// names them), with the count key the audit record uses.
var tenantPurgeDeletes = []struct {
	key string
	q   string
}{
	{"storeEntries", "DELETE FROM store_entries WHERE incarnation IN (SELECT incarnation FROM stores WHERE tenant_id=?)"},
	{"stores", "DELETE FROM stores WHERE tenant_id=?"},
	{"queueMessages", "DELETE FROM queue_messages WHERE incarnation IN (SELECT incarnation FROM queues WHERE tenant_id=?)"},
	{"queues", "DELETE FROM queues WHERE tenant_id=?"},
	{"revocations", "DELETE FROM revocations WHERE object_id IN (SELECT id FROM objects WHERE tenant_id=?)"},
	{"objects", "DELETE FROM objects WHERE tenant_id=?"},
	{"deployments", "DELETE FROM deployments WHERE tenant_id=?"},
	{"workloadPublic", "DELETE FROM workload_public WHERE tenant_id=?"},
	{"publicNames", "DELETE FROM public_names WHERE tenant_id=?"},
	{"containerReservations", "DELETE FROM container_reservations WHERE tenant_id=?"},
	{"warmKeys", "DELETE FROM warm_keys WHERE tenant_id=?"},
	{"hostPaths", "DELETE FROM host_paths WHERE tenant_id=?"},
	{"throttle", "DELETE FROM tenant_throttle WHERE tenant_id=?"},
	{"custodyNames", "DELETE FROM custody_names WHERE tenant_id=?"},
}

func (s *SQLStore) PurgeTenant(ctx context.Context, id string, now int64) (map[string]int64, []string, error) {
	var counts map[string]int64
	var indexed []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		counts = map[string]int64{}
		var exists int
		if err := tx.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM tenants WHERE id=?"), id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		var err error
		if indexed, err = s.column(ctx, tx, "SELECT name FROM custody_names WHERE tenant_id=? ORDER BY name", id); err != nil {
			return err
		}
		for _, d := range tenantPurgeDeletes {
			res, err := tx.ExecContext(ctx, s.q(d.q), id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				counts[d.key] = n
			}
		}
		res, err := tx.ExecContext(ctx, s.q("DELETE FROM secret_choice WHERE name=?"), oboChoiceName(id))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			counts["secretChoices"] = n
		}
		hosts, err := s.column(ctx, tx, "SELECT id FROM hosts ORDER BY id")
		if err != nil {
			return err
		}
		for _, h := range hosts {
			if _, err := tx.ExecContext(ctx, s.q("INSERT INTO tenant_purges (host_id, tenant_id, created_at) VALUES (?,?,?) ON CONFLICT (host_id, tenant_id) DO NOTHING"),
				h, id, now); err != nil {
				return err
			}
		}
		counts["hostsToPurge"] = int64(len(hosts))
		return s.deleteTenantTx(ctx, tx, id)
	})
	return counts, indexed, err
}

func (s *SQLStore) TenantPurges(ctx context.Context, hostID string) ([]string, error) {
	return s.column(ctx, s.db, "SELECT tenant_id FROM tenant_purges WHERE host_id=? ORDER BY created_at, tenant_id", hostID)
}

func (s *SQLStore) AckTenantPurge(ctx context.Context, hostID, tenantID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.q("DELETE FROM tenant_purges WHERE host_id=? AND tenant_id=?"), hostID, tenantID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *SQLStore) IndexCustodyName(ctx context.Context, name, tenantID string, now int64) error {
	_, err := s.db.ExecContext(ctx, s.q("INSERT INTO custody_names (name, tenant_id, created_at) VALUES (?,?,?) ON CONFLICT (name) DO NOTHING"),
		name, tenantID, now)
	return err
}

func (s *SQLStore) UnindexCustodyName(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, s.q("DELETE FROM custody_names WHERE name=?"), name)
	return err
}
