package mgmt

import (
	"context"
	"database/sql"
	"errors"
)

// queryer is what *sql.DB and *sql.Tx share for reads.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AuditSnapshot is one chain as VerifyChain checks it: its records (in
// seq order), its head, and its retention checkpoint (retention.go).
type AuditSnapshot struct {
	Records  []AuditRecord
	HeadSeq  int64
	HeadHash string
	// Checkpoint is the last record audit retention deleted (zero when
	// nothing was): the first record kept is Checkpoint.Seq+1 and links
	// to Checkpoint.Hash.
	Checkpoint AuditCheckpoint
}

// AuditCheckpoint is a chain's retention checkpoint.
type AuditCheckpoint struct {
	Seq    int64  `json:"seq"`
	Hash   string `json:"hash"`
	Pruned int64  `json:"pruned"`
	At     int64  `json:"at"`
}

// AuditChainSnapshot reads a chain's records, its head and its retention
// checkpoint in one read transaction. AppendAudit writes the record and
// moves the head in one transaction; reading them in two separate queries
// (as VerifyChain once did) let an append commit in between, and an intact
// chain then failed verification with "chain head mismatch" (#93). The
// pruner deletes records and moves the checkpoint in one transaction too.
//
// Postgres: REPEATABLE READ, so every read sees the snapshot taken at the
// first one. SQLite: the store has a single connection, so no append can
// run while the transaction holds it.
func (s *SQLStore) AuditChainSnapshot(ctx context.Context, chain string) (AuditSnapshot, error) {
	var snap AuditSnapshot
	var opts *sql.TxOptions
	if s.d == dialectPostgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return snap, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, s.q("SELECT seq, hash FROM audit_heads WHERE chain=?"), chain).Scan(&snap.HeadSeq, &snap.HeadHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snap, err
	}
	if snap.Checkpoint, err = s.auditCheckpoint(ctx, tx, chain); err != nil {
		return snap, err
	}
	if snap.Records, err = s.listAudit(ctx, tx, AuditFilter{Chain: chain}); err != nil {
		return snap, err
	}
	return snap, nil
}
