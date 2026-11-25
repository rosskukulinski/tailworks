package mgmt

import (
	"context"
	"time"
)

// Audit retention (issue #120). By default mgmt keeps every audit record
// forever: the audit log is the record of who did what, and it is
// hash-chained, so deleting from it is something an operator opts into
// (tw-mgmt -audit-max-age, -audit-max-records; tw up --audit-max-age,
// --audit-max-records).
//
// With retention on, a pruner deletes each chain's oldest records: those
// older than the max age, and all but the newest max-records. Deleting
// the start of a hash chain would leave the first record kept linking to
// a hash nobody can recompute, so the pruner keeps a checkpoint per chain
// (audit_checkpoints): the seq and hash of the last record it deleted,
// written in the same transaction as the delete. VerifyChain starts from
// the checkpoint instead of from an empty hash, so the records that remain
// verify exactly as before, a gap or an edit among them is still caught,
// and so is a truncated tail (the head check). What retention gives up is
// the proof that the deleted records existed; the checkpoint records that
// they did, how many there were (pruned), and every prune is itself an
// audit record (audit_pruned, on the mgmt chain) naming the chain, the
// last seq deleted and its hash.
//
// The pruner works in batches (auditPruneBatch records per transaction),
// so a first prune of a long chain doesn't hold the head lock for long.

const (
	// AuditPruneInterval is how often the pruner runs (also once at start).
	AuditPruneInterval = time.Hour
	// auditPruneBatch is how many records one prune transaction deletes.
	auditPruneBatch = 5000
)

// auditPruner is implemented by stores that can prune audit chains
// (SQLStore; sqlstore_retention.go).
type auditPruner interface {
	AuditPruneTarget(ctx context.Context, chain, cutoff string, keep int64) (int64, error)
	PruneAudit(ctx context.Context, chain string, through, batch, now int64) (int64, AuditCheckpoint, error)
}

// startRetention runs the pruner when retention is configured.
func (m *Mgmt) startRetention() {
	if m.cfg.AuditMaxAge <= 0 && m.cfg.AuditMaxRecords <= 0 {
		return
	}
	if _, ok := m.store.(auditPruner); !ok {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		wait := time.Duration(0)
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-m.clock.After(wait):
			}
			wait = AuditPruneInterval
			if _, err := m.PruneAudit(m.ctx); err != nil && m.ctx.Err() == nil {
				m.log.Warn("audit retention", "err", err)
			}
		}
	}()
}

// PruneAudit applies audit retention to every chain once and returns how
// many records it deleted per chain.
func (m *Mgmt) PruneAudit(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	ap, ok := m.store.(auditPruner)
	if !ok || (m.cfg.AuditMaxAge <= 0 && m.cfg.AuditMaxRecords <= 0) {
		return out, nil
	}
	chains, err := m.store.AuditChains(ctx)
	if err != nil {
		return out, err
	}
	now := m.now()
	cutoff := ""
	if m.cfg.AuditMaxAge > 0 {
		cutoff = now.Add(-m.cfg.AuditMaxAge).UTC().Format(time.RFC3339)
	}
	for _, c := range chains {
		through, err := ap.AuditPruneTarget(ctx, c, cutoff, m.cfg.AuditMaxRecords)
		if err != nil {
			return out, err
		}
		var cp AuditCheckpoint
		for {
			n, next, err := ap.PruneAudit(ctx, c, through, auditPruneBatch, now.UnixMilli())
			if err != nil {
				return out, err
			}
			if n == 0 {
				break
			}
			out[c] += n
			cp = next
			if cp.Seq >= through || ctx.Err() != nil {
				break
			}
		}
		if out[c] > 0 {
			m.audit(ctx, AuditRecord{Kind: "audit_pruned"}, map[string]any{"chain": c, "pruned": out[c], "through": cp.Seq, "throughHash": cp.Hash,
				"prunedTotal": cp.Pruned, "maxAge": m.cfg.AuditMaxAge.String(), "maxRecords": m.cfg.AuditMaxRecords})
			m.log.Info("audit retention", "chain", c, "pruned", out[c], "through", cp.Seq)
		}
	}
	return out, nil
}
