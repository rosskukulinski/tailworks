package mgmt

import (
	"context"
	"database/sql"
	"errors"
)

// Degraded platform stores (#147), SQL side: the degraded record is five
// columns of the store's row (migration 202611211922_store_degraded).
const storeDegradedCols = "degraded_seq, degraded_error, degraded_host, degraded_at, degraded_workload"

// errStoreNotDegraded: a repair named an entry the store is not recorded
// as degraded at.
var errStoreNotDegraded = errors.New("store_not_degraded")

// storeDegradedJournal is implemented by stores that keep platform stores'
// degraded records (SQLStore).
type storeDegradedJournal interface {
	// UpdateStoreDegraded sets the degraded record of the store of
	// incarnation inc to what decide returns from the current one (write
	// false: leave it). ErrNotFound if there is no such store, or it has
	// another incarnation. Returns the record before.
	// It is not a write: updatedAt stays.
	UpdateStoreDegraded(ctx context.Context, tenantID, workload, kind, name, inc string,
		decide func(cur *StoreDegraded) (next *StoreDegraded, write bool)) (*StoreDegraded, error)
	// SkipStoreEntry deletes journal entry seq of a store recorded as
	// degraded at seq (errStoreNotDegraded otherwise) and clears the
	// record. ErrNotFound: no such store.
	SkipStoreEntry(ctx context.Context, tenantID, workload, kind, name string, seq, now int64) (StoreInfo, error)
	// ListDegradedStores lists the stores with a degraded record (tenantID
	// "": every tenant), by (tenant, workload, kind, name), at most limit.
	ListDegradedStores(ctx context.Context, tenantID string, limit int) ([]StoreInfo, error)
}

func degradedArgs(d *StoreDegraded) []any {
	if d == nil {
		return []any{0, "", "", 0, ""}
	}
	return []any{d.Seq, d.Error, d.Host, d.At, d.Workload}
}

func (s *SQLStore) UpdateStoreDegraded(ctx context.Context, tenantID, workload, kind, name, inc string,
	decide func(cur *StoreDegraded) (*StoreDegraded, bool)) (*StoreDegraded, error) {
	var prev *StoreDegraded
	err := s.tx(ctx, func(tx *sql.Tx) error {
		st, err := scanStore(tx.QueryRowContext(ctx, s.q("SELECT "+storeCols+" FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"+s.forUpdate()),
			tenantID, workload, kind, name))
		if err != nil {
			return err
		}
		if st.Incarnation != inc {
			return ErrNotFound
		}
		prev = st.Degraded
		next, write := decide(st.Degraded)
		if !write {
			return nil
		}
		args := append(degradedArgs(next), tenantID, workload, kind, name)
		_, err = tx.ExecContext(ctx, s.q("UPDATE stores SET degraded_seq=?, degraded_error=?, degraded_host=?, degraded_at=?, degraded_workload=? WHERE tenant_id=? AND workload=? AND kind=? AND name=?"), args...)
		return err
	})
	return prev, err
}

func (s *SQLStore) SkipStoreEntry(ctx context.Context, tenantID, workload, kind, name string, seq, now int64) (StoreInfo, error) {
	var st StoreInfo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = scanStore(tx.QueryRowContext(ctx, s.q("SELECT "+storeCols+" FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"+s.forUpdate()),
			tenantID, workload, kind, name))
		if err != nil {
			return err
		}
		if st.Degraded == nil || st.Degraded.Seq != seq {
			return errStoreNotDegraded
		}
		var size int64
		switch err := tx.QueryRowContext(ctx, s.q("SELECT size FROM store_entries WHERE incarnation=? AND seq=?"), st.Incarnation, seq).Scan(&size); {
		case errors.Is(err, sql.ErrNoRows):
			// Already gone (compacted by a later entry with its ckey):
			// only the record is left to clear.
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, s.q("DELETE FROM store_entries WHERE incarnation=? AND seq=?"), st.Incarnation, seq); err != nil {
				return err
			}
			st.Entries--
			st.Bytes -= size
		}
		st.Degraded = nil
		st.UpdatedAt = now
		args := append([]any{st.Entries, st.Bytes}, degradedArgs(nil)...)
		args = append(args, now, tenantID, workload, kind, name)
		_, err = tx.ExecContext(ctx, s.q("UPDATE stores SET entries=?, bytes=?, degraded_seq=?, degraded_error=?, degraded_host=?, degraded_at=?, degraded_workload=?, updated_at=? WHERE tenant_id=? AND workload=? AND kind=? AND name=?"), args...)
		return err
	})
	return st, err
}

func (s *SQLStore) ListDegradedStores(ctx context.Context, tenantID string, limit int) ([]StoreInfo, error) {
	q := "SELECT " + storeCols + " FROM stores WHERE degraded_seq > 0"
	var args []any
	if tenantID != "" {
		q += " AND tenant_id=?"
		args = append(args, tenantID)
	}
	q += " ORDER BY tenant_id, workload, kind, name LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoreInfo
	for rows.Next() {
		st, err := scanStore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
