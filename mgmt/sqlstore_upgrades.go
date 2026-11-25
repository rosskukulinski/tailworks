package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Releases, upgrade runs and restores (ADR-041 §8).

// upgradeStore is implemented by stores that hold the release catalog and
// upgrade runs (SQLStore).
type upgradeStore interface {
	PutRelease(ctx context.Context, version, entryJSON, by string, at int64) error
	ImportedReleases(ctx context.Context) (map[string]string, error) // version -> entry JSON
	// CreateUpgradeRun inserts a run unless one is active (running or
	// paused): ErrExists.
	CreateUpgradeRun(ctx context.Context, id, to, state, runJSON string, at int64) error
	GetUpgradeRun(ctx context.Context, id string) (runJSON string, updatedAt int64, err error)
	ListUpgradeRuns(ctx context.Context, limit int) ([]string, error)
	// UpdateUpgradeRun replaces a run if it was last written at prev
	// (errStoreConflict otherwise).
	UpdateUpgradeRun(ctx context.Context, id, state, runJSON string, prev, at int64) error
	ActiveUpgradeRuns(ctx context.Context) ([]string, error)
	ApplyDeferredContract(ctx context.Context) ([]string, error)
	RestoreFence(ctx context.Context) (int64, error)
}

var _ upgradeStore = (*SQLStore)(nil)

func (s *SQLStore) PutRelease(ctx context.Context, version, entryJSON, by string, at int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO releases (version, entry, imported_at, imported_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (version) DO UPDATE SET entry=excluded.entry, imported_at=excluded.imported_at, imported_by=excluded.imported_by`),
		version, entryJSON, at, by)
	return err
}

func (s *SQLStore) ImportedReleases(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT version, entry FROM releases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var v, e string
		if err := rows.Scan(&v, &e); err != nil {
			return nil, err
		}
		out[v] = e
	}
	return out, rows.Err()
}

var activeRunStates = "('running','paused')"

func (s *SQLStore) CreateUpgradeRun(ctx context.Context, id, to, state, runJSON string, at int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM upgrade_runs WHERE state IN "+activeRunStates).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExists
		}
		_, err := tx.ExecContext(ctx, s.q("INSERT INTO upgrade_runs (id, to_version, state, run, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)"),
			id, to, state, runJSON, at, at)
		return err
	})
}

func (s *SQLStore) GetUpgradeRun(ctx context.Context, id string) (string, int64, error) {
	var run string
	var at int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT run, updated_at FROM upgrade_runs WHERE id=?"), id).Scan(&run, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return run, at, err
}

func (s *SQLStore) ListUpgradeRuns(ctx context.Context, limit int) ([]string, error) {
	return s.runs(ctx, fmt.Sprintf("SELECT run FROM upgrade_runs ORDER BY created_at DESC, id DESC LIMIT %d", limit))
}

func (s *SQLStore) ActiveUpgradeRuns(ctx context.Context) ([]string, error) {
	return s.runs(ctx, "SELECT run FROM upgrade_runs WHERE state IN "+activeRunStates+" ORDER BY created_at")
}

func (s *SQLStore) runs(ctx context.Context, q string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLStore) UpdateUpgradeRun(ctx context.Context, id, state, runJSON string, prev, at int64) error {
	if at <= prev {
		at = prev + 1 // updated_at is the CAS token: it must move
	}
	r, err := s.db.ExecContext(ctx, s.q("UPDATE upgrade_runs SET state=?, run=?, updated_at=? WHERE id=? AND updated_at=?"),
		state, runJSON, at, id, prev)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return errStoreConflict
	}
	return nil
}

// ApplyDeferredContract applies the contract migrations that opening the
// store deferred because an older replica was live (migrate() applies
// expand migrations at once). Called by an upgrade run once every replica
// reports this version, or by `tw-mgmt migrate --apply`.
func (s *SQLStore) ApplyDeferredContract(ctx context.Context) ([]string, error) {
	if s.d == dialectPostgres {
		if _, err := s.db.ExecContext(ctx, "SELECT pg_advisory_lock(7464)"); err != nil {
			return nil, err
		}
		defer s.db.ExecContext(context.Background(), "SELECT pg_advisory_unlock(7464)")
	}
	before, err := s.SchemaStatus(ctx)
	if err != nil {
		return nil, err
	}
	if len(before.PendingContract) == 0 {
		return nil, nil
	}
	if err := s.applyMigrations(ctx, Migrations(), StoreBinaryVersion, true); err != nil {
		return nil, err
	}
	return before.PendingContract, nil
}

// RestoreEpochJump is how far every lease epoch jumps at a restore: far
// past any epoch handed out between the backup and the restore (a lease's
// epoch grows by one per move), so no fencing token (ADR-028, lock tokens
// built from epochs) is ever reused.
const RestoreEpochJump = 1 << 20

// RestoreFence runs after a store is restored from a backup (`tw-mgmt
// restore-fence`, `tw upgrade` rollback): it raises every lease's epoch
// by RestoreEpochJump and counts the restore. It returns the new restore
// generation.
func (s *SQLStore) RestoreFence(ctx context.Context) (int64, error) {
	var gen int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.q("UPDATE leases SET epoch = epoch + ?"), RestoreEpochJump); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("UPDATE deployments SET epoch = epoch + ?"), RestoreEpochJump); err != nil {
			return err
		}
		var cur string
		err := tx.QueryRowContext(ctx, s.q("SELECT v FROM schema_meta WHERE k=?"), metaRestoreGeneration).Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		fmt.Sscan(cur, &gen)
		gen++
		_, err = tx.ExecContext(ctx, s.q("INSERT INTO schema_meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v=excluded.v"),
			metaRestoreGeneration, fmt.Sprint(gen))
		return err
	})
	return gen, err
}

// RestoreFenceDSN is RestoreFence on a store that is not opened for
// serving: it doesn't migrate, so a restored store from an older tw-mgmt
// stays at its schema (only the bookkeeping tables are created).
func RestoreFenceDSN(ctx context.Context, dsn string) (int64, error) {
	s, err := openSQL(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer s.db.Close()
	if err := s.ensureSchemaTables(ctx); err != nil {
		return 0, err
	}
	return s.RestoreFence(ctx)
}
