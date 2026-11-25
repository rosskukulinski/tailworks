package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Platform stores (ADR-044): one row per store, and its
// journal. Entries are keyed by the store's incarnation (random per
// creation), so deleting a store and creating it again never mixes their
// entries, and a delete is two statements.
const schemaStores = `
CREATE TABLE IF NOT EXISTS stores (
  tenant_id TEXT NOT NULL, workload TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL,
  incarnation TEXT NOT NULL, head BIGINT NOT NULL, entries BIGINT NOT NULL, bytes BIGINT NOT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, workload, kind, name));
CREATE TABLE IF NOT EXISTS store_entries (
  incarnation TEXT NOT NULL, seq BIGINT NOT NULL, ckey TEXT NOT NULL DEFAULT '', size BIGINT NOT NULL,
  data %s NOT NULL, PRIMARY KEY (incarnation, seq));
CREATE INDEX IF NOT EXISTS store_entries_ckey ON store_entries (incarnation, ckey);
`

// errStoreConflict: the append's after/incarnation is not the head.
var errStoreConflict = errors.New("store_conflict")

// errStoreFull: the append would take the store over its byte limit.
var errStoreFull = errors.New("store_limit")

// storeJournal is implemented by stores that can hold platform stores
// (SQLStore).
type storeJournal interface {
	// CreateStore inserts st unless a store of that (tenant, workload,
	// kind, name) exists; created reports whether it did.
	CreateStore(ctx context.Context, st StoreInfo) (created bool, err error)
	// GetStore returns one store (ErrNotFound).
	GetStore(ctx context.Context, tenantID, workload, kind, name string) (StoreInfo, error)
	// ListStores lists a workload's stores by (kind, name); kind "" = all.
	// PageKey.ID is "kind/name".
	ListStores(ctx context.Context, tenantID, workload, kind string, pg Page) ([]StoreInfo, error)
	// DeleteStore deletes a store and its entries (ErrNotFound).
	DeleteStore(ctx context.Context, tenantID, workload, kind, name string) (StoreInfo, error)
	// AppendStore appends entries if the store's incarnation is inc and its
	// head is after (errStoreConflict otherwise), giving them after+1….
	// seal turns an entry's plaintext into what is stored, given its seq.
	// An entry with a ckey deletes every earlier entry with that ckey.
	// errStoreFull if the kept bytes would exceed maxBytes; a *quotaError
	// if an append that grows the store would take the tenant's stores
	// past tenantMax (0 = no tenant quota; quota.go).
	AppendStore(ctx context.Context, tenantID, workload, kind, name, inc string, after int64, entries []storeEntryIn,
		seal func(seq int64, data []byte) ([]byte, error), maxBytes, tenantMax, now int64) (StoreInfo, error)
	// ReadStore returns the entries of incarnation inc with seq > after (at
	// most limit, and stopping once maxBytes of stored data are read), and
	// whether more remain.
	ReadStore(ctx context.Context, inc string, after int64, limit int, maxBytes int64) ([]storeEntryRow, bool, error)
}

// storeEntryIn is one entry to append: its compaction key (already
// hashed) and plaintext.
type storeEntryIn struct {
	CKey string
	Data []byte
}

// storeEntryRow is one stored (sealed) entry.
type storeEntryRow struct {
	Seq  int64
	Data []byte
}

// storeInsertCols are the columns a new store row sets; storeCols adds the
// degraded record (sqlstore_storedegraded.go).
const storeInsertCols = "tenant_id, workload, kind, name, incarnation, head, entries, bytes, created_at, updated_at"
const storeCols = storeInsertCols + ", " + storeDegradedCols

func scanStore(sc interface{ Scan(...any) error }) (StoreInfo, error) {
	var st StoreInfo
	var d StoreDegraded
	err := sc.Scan(&st.TenantID, &st.Workload, &st.Kind, &st.Name, &st.Incarnation, &st.Head, &st.Entries, &st.Bytes, &st.CreatedAt, &st.UpdatedAt,
		&d.Seq, &d.Error, &d.Host, &d.At, &d.Workload)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	if d.Seq > 0 {
		st.Degraded = &d
	}
	return st, err
}

func (s *SQLStore) CreateStore(ctx context.Context, st StoreInfo) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.q("INSERT INTO stores ("+storeInsertCols+") VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING"),
		st.TenantID, st.Workload, st.Kind, st.Name, st.Incarnation, st.Head, st.Entries, st.Bytes, st.CreatedAt, st.UpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQLStore) GetStore(ctx context.Context, tenantID, workload, kind, name string) (StoreInfo, error) {
	return scanStore(s.db.QueryRowContext(ctx, s.q("SELECT "+storeCols+" FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"),
		tenantID, workload, kind, name))
}

func (s *SQLStore) ListStores(ctx context.Context, tenantID, workload, kind string, pg Page) ([]StoreInfo, error) {
	q := "SELECT " + storeCols + " FROM stores WHERE tenant_id=? AND workload=?"
	args := []any{tenantID, workload}
	if kind != "" {
		q += " AND kind=?"
		args = append(args, kind)
	}
	if pg.After != nil {
		k, n, _ := strings.Cut(pg.After.ID, "/")
		q += " AND (kind > ? OR (kind = ? AND name > ?))"
		args = append(args, k, k, n)
	}
	q += " ORDER BY kind, name"
	if pg.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, pg.Limit)
	}
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

func (s *SQLStore) DeleteStore(ctx context.Context, tenantID, workload, kind, name string) (StoreInfo, error) {
	var st StoreInfo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = scanStore(tx.QueryRowContext(ctx, s.q("SELECT "+storeCols+" FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"+s.forUpdate()),
			tenantID, workload, kind, name))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM store_entries WHERE incarnation=?"), st.Incarnation); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q("DELETE FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"), tenantID, workload, kind, name)
		return err
	})
	return st, err
}

func (s *SQLStore) AppendStore(ctx context.Context, tenantID, workload, kind, name, inc string, after int64, entries []storeEntryIn,
	seal func(seq int64, data []byte) ([]byte, error), maxBytes, tenantMax, now int64) (StoreInfo, error) {
	var st StoreInfo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = scanStore(tx.QueryRowContext(ctx, s.q("SELECT "+storeCols+" FROM stores WHERE tenant_id=? AND workload=? AND kind=? AND name=?"+s.forUpdate()),
			tenantID, workload, kind, name))
		if err != nil {
			return err
		}
		if st.Incarnation != inc || st.Head != after {
			return errStoreConflict
		}
		before := st.Bytes
		for _, e := range entries {
			st.Head++
			if e.CKey != "" {
				var n, size int64
				if err := tx.QueryRowContext(ctx, s.q("SELECT COUNT(*), COALESCE(SUM(size), 0) FROM store_entries WHERE incarnation=? AND ckey=?"),
					inc, e.CKey).Scan(&n, &size); err != nil {
					return err
				}
				if n > 0 {
					if _, err := tx.ExecContext(ctx, s.q("DELETE FROM store_entries WHERE incarnation=? AND ckey=?"), inc, e.CKey); err != nil {
						return err
					}
					st.Entries -= n
					st.Bytes -= size
				}
			}
			sealed, err := seal(st.Head, e.Data)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.q("INSERT INTO store_entries (incarnation, seq, ckey, size, data) VALUES (?,?,?,?,?)"),
				inc, st.Head, e.CKey, len(e.Data), sealed); err != nil {
				return err
			}
			st.Entries++
			st.Bytes += int64(len(e.Data))
		}
		if st.Bytes > maxBytes {
			return errStoreFull
		}
		if err := s.checkTenantStoreBytes(ctx, tx, st, before, tenantMax); err != nil { // sqlstore_quota.go
			return err
		}
		st.UpdatedAt = now
		_, err = tx.ExecContext(ctx, s.q("UPDATE stores SET head=?, entries=?, bytes=?, updated_at=? WHERE tenant_id=? AND workload=? AND kind=? AND name=?"),
			st.Head, st.Entries, st.Bytes, now, tenantID, workload, kind, name)
		return err
	})
	return st, err
}

func (s *SQLStore) ReadStore(ctx context.Context, inc string, after int64, limit int, maxBytes int64) ([]storeEntryRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, s.q("SELECT seq, data FROM store_entries WHERE incarnation=? AND seq>? ORDER BY seq LIMIT ?"), inc, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []storeEntryRow
	var total int64
	for rows.Next() {
		if len(out) == limit || (len(out) > 0 && total >= maxBytes) {
			return out, true, nil
		}
		var e storeEntryRow
		if err := rows.Scan(&e.Seq, &e.Data); err != nil {
			return nil, false, err
		}
		total += int64(len(e.Data))
		out = append(out, e)
	}
	return out, false, rows.Err()
}
