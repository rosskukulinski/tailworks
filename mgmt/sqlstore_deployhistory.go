package mgmt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// Deployment history and bundle retention (ADR-046), SQL side.
// The columns are migrations/202611152330_deploy_history.sql.

var _ deployHistoryStore = (*SQLStore)(nil)

const historyCols = "id, tenant_id, workload, host_id, epoch, created_at, deployed_by, stopped_at, host_status, host_error, " +
	"bundle_sha256, bundle_pruned_at, active_at, rollback_of, COALESCE(length(bundle), 0)"

func scanHistory(sc interface{ Scan(...any) error }) (DeploymentRecord, error) {
	var d DeploymentRecord
	var pruned int64
	err := sc.Scan(&d.ID, &d.TenantID, &d.Workload, &d.HostID, &d.Epoch, &d.CreatedAt, &d.DeployedBy, &d.StoppedAt,
		&d.HostStatus, &d.Error, &d.BundleSHA256, &pruned, &d.ActiveAt, &d.RollbackOf, &d.BundleBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.BundleRetained = pruned == 0 && d.BundleBytes > 0
	d.BundlePruned = pruned != 0
	return d, err
}

// DeploymentHistory lists a workload's deployments newest first, ordered by
// (created_at, id) descending; pg.After is the last row of the previous
// page (exclusive), so the next page holds older rows.
func (s *SQLStore) DeploymentHistory(ctx context.Context, tenantID, workload string, pg Page) ([]DeploymentRecord, error) {
	where := []string{"tenant_id=?", "workload=?"}
	args := []any{tenantID, workload}
	if pg.After != nil {
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, pg.After.Created, pg.After.Created, pg.After.ID)
	}
	q := "SELECT " + historyCols + " FROM deployments WHERE " + strings.Join(where, " AND ") + " ORDER BY created_at DESC, id DESC" + limitClause(pg)
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentRecord
	for rows.Next() {
		d, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeploymentRecordByID is one deployment's history record.
func (s *SQLStore) DeploymentRecordByID(ctx context.Context, id string) (DeploymentRecord, error) {
	return scanHistory(s.db.QueryRowContext(ctx, s.q("SELECT "+historyCols+" FROM deployments WHERE id=?"), id))
}

// SetDeploymentMeta records a deployment's bundle hash and, for a rollback,
// the deployment it redeployed. An empty value never overwrites a recorded
// one: a history read backfills the hash of a record it found without one
// (fillRecord) with the rollbackOf it read, and when that read raced the
// deploy's own write (between the record's commit and afterDeploy), its
// late write used to erase the rollback's rollbackOf for good (#563).
func (s *SQLStore) SetDeploymentMeta(ctx context.Context, id, bundleSHA256, rollbackOf string) error {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET
		bundle_sha256 = CASE WHEN CAST(? AS TEXT) = '' THEN bundle_sha256 ELSE ? END,
		rollback_of = CASE WHEN CAST(? AS TEXT) = '' THEN rollback_of ELSE ? END
		WHERE id=?`), bundleSHA256, bundleSHA256, rollbackOf, rollbackOf, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDeploymentActive records when the host first reported the deployment
// active. Only the host it is assigned to counts (as SetDeploymentStatus).
// Heartbeats repeat "active" every few seconds, so it reads first and
// writes only the first time.
func (s *SQLStore) MarkDeploymentActive(ctx context.Context, id, hostID string, now int64) error {
	var at int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT active_at FROM deployments WHERE id=? AND host_id=?"), id, hostID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) || err == nil && at != 0 {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q("UPDATE deployments SET active_at=? WHERE id=? AND host_id=? AND active_at=0"), now, id, hostID)
	return err
}

// PruneBundles drops the bundles of a workload's deployments beyond the
// newest keep that still hold one (keep <= 0: prune nothing). The hash is
// recorded first, so history keeps it. It returns the pruned IDs.
func (s *SQLStore) PruneBundles(ctx context.Context, tenantID, workload string, keep int, now int64) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, bundle_sha256 FROM deployments
		WHERE tenant_id=? AND workload=? AND bundle_pruned_at=0 AND bundle IS NOT NULL AND length(bundle) > 0
		ORDER BY created_at DESC, id DESC`), tenantID, workload)
	if err != nil {
		return nil, err
	}
	type cand struct{ id, sha string }
	var cs []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.sha); err != nil {
			rows.Close()
			return nil, err
		}
		cs = append(cs, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cs) <= keep {
		return nil, nil
	}
	var pruned []string
	for _, c := range cs[keep:] {
		if c.sha == "" {
			var b []byte
			if err := s.db.QueryRowContext(ctx, s.q("SELECT bundle FROM deployments WHERE id=?"), c.id).Scan(&b); err != nil {
				return pruned, err
			}
			c.sha = bundleSHA256(b)
		}
		res, err := s.db.ExecContext(ctx, s.q("UPDATE deployments SET bundle=NULL, bundle_pruned_at=?, bundle_sha256=? WHERE id=? AND bundle_pruned_at=0"),
			now, c.sha, c.id)
		if err != nil {
			return pruned, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			pruned = append(pruned, c.id)
		}
	}
	return pruned, nil
}

// bundleSHA256 is the hex SHA-256 of a bundle ("" for none).
func bundleSHA256(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
