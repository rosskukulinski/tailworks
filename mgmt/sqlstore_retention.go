package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Audit retention on the audit tables (retention.go).

func (s *SQLStore) auditCheckpoint(ctx context.Context, qr queryer, chain string) (AuditCheckpoint, error) {
	var cp AuditCheckpoint
	err := qr.QueryRowContext(ctx, s.q("SELECT seq, hash, pruned, at FROM audit_checkpoints WHERE chain=?"), chain).Scan(&cp.Seq, &cp.Hash, &cp.Pruned, &cp.At)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditCheckpoint{}, nil
	}
	return cp, err
}

// AuditPruneTarget is the last seq of chain that retention may delete:
// every record but the newest keep (keep > 0), and every record before
// the first one whose ts is at or after cutoff (cutoff != ""; RFC 3339
// UTC, compared as text). The age rule stops at the first record that
// isn't old enough, so a record with a skewed or odd timestamp is kept
// along with everything after it, never skipped over.
func (s *SQLStore) AuditPruneTarget(ctx context.Context, chain, cutoff string, keep int64) (int64, error) {
	var head int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT seq FROM audit_heads WHERE chain=?"), chain).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var through int64
	if keep > 0 && head > keep {
		through = head - keep
	}
	if cutoff != "" {
		var first int64
		err := s.db.QueryRowContext(ctx, s.q("SELECT seq FROM audit WHERE chain=? AND ts >= ? ORDER BY seq LIMIT 1"), chain, cutoff).Scan(&first)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			through = head // every record is older
		case err != nil:
			return 0, err
		default:
			through = max(through, first-1)
		}
	}
	return through, nil
}

// PruneAudit deletes chain's records up to through (at most batch of them
// past the current checkpoint) and moves the checkpoint to the last one
// deleted, in one transaction. It returns how many it deleted and the new
// checkpoint. The chain's head row is locked first, as AppendAudit does,
// so a prune and an append never interleave.
func (s *SQLStore) PruneAudit(ctx context.Context, chain string, through, batch, now int64) (int64, AuditCheckpoint, error) {
	var n int64
	var cp AuditCheckpoint
	err := s.tx(withoutIntents(ctx), func(tx *sql.Tx) error {
		var head int64
		var err error
		if err := tx.QueryRowContext(ctx, s.q("SELECT seq FROM audit_heads WHERE chain=?"+s.forUpdate()), chain).Scan(&head); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if cp, err = s.auditCheckpoint(ctx, tx, chain); err != nil {
			return err
		}
		through = min(through, head)
		if batch > 0 {
			through = min(through, cp.Seq+batch)
		}
		if through <= cp.Seq {
			return nil
		}
		var hash string
		if err := tx.QueryRowContext(ctx, s.q("SELECT hash FROM audit WHERE chain=? AND seq=?"), chain, through).Scan(&hash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("audit chain %s has no record %d (head %d, checkpoint %d): not pruning a chain with a gap", chain, through, head, cp.Seq)
			}
			return err
		}
		if err := tx.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM audit WHERE chain=? AND seq<=?"), chain, through).Scan(&n); err != nil {
			return err
		}
		// The checkpoint first: the append-only trigger lets a record be
		// deleted only once a checkpoint covers it (sqlstore.go).
		cp = AuditCheckpoint{Seq: through, Hash: hash, Pruned: cp.Pruned + n, At: now}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO audit_checkpoints (chain, seq, hash, pruned, at) VALUES (?,?,?,?,?)
			ON CONFLICT (chain) DO UPDATE SET seq=excluded.seq, hash=excluded.hash, pruned=excluded.pruned, at=excluded.at`),
			chain, cp.Seq, cp.Hash, cp.Pruned, cp.At); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM audit_workloads WHERE chain=? AND seq<=?"), chain, through); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q("DELETE FROM audit WHERE chain=? AND seq<=?"), chain, through)
		return err
	})
	return n, cp, err
}
