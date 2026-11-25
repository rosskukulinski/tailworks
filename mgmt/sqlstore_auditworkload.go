package mgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Issue #512: the list filters /v1 applies so its clients (the admin UI)
// don't page and filter themselves: GET /v1/objects?tag=&human= and
// GET /v1/audit?workload=.

// objectTagCond is the WHERE condition for ObjectFilter.Tag: objects.tags
// is a JSON array of strings (jsonStr), so the tag's JSON encoding, quotes
// included, occurs in it exactly when the array holds that element.
func (s *SQLStore) objectTagCond(tag string) (string, any) {
	needle := jsonStr(tag)
	if s.d == dialectPostgres {
		return "strpos(tags, ?) > 0", needle
	}
	return "instr(tags, ?) > 0", needle
}

// auditWorkloadTx is the workload rec is about, stored in audit_workloads
// (migration 202611251900_audit_workload): its object's workload, else, for
// a mgmt record, the "workload" its detail names (deploy, undeploy,
// secrets). Host and tenant-written detail is never trusted for it. rec.Raw
// must be set (the stored body).
func (s *SQLStore) auditWorkloadTx(ctx context.Context, tx *sql.Tx, rec AuditRecord) (string, error) {
	if rec.ObjectID != "" {
		var w string
		err := tx.QueryRowContext(ctx, s.q("SELECT workload FROM objects WHERE id=?"), rec.ObjectID).Scan(&w)
		if err == nil {
			return w, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if rec.Chain != "mgmt" {
		return "", nil
	}
	var body struct {
		Detail struct {
			Workload string `json:"workload"`
		} `json:"detail"`
	}
	if json.Unmarshal(rec.Raw, &body) != nil {
		return "", nil
	}
	return body.Detail.Workload, nil
}

// putAuditWorkloadTx records rec's workload (if any) beside it. audit rows
// are append-only, so this lives in its own table, keyed like audit.
func (s *SQLStore) putAuditWorkloadTx(ctx context.Context, tx *sql.Tx, rec AuditRecord, workload string) error {
	if workload == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.q("INSERT INTO audit_workloads (chain, seq, workload) VALUES (?,?,?)"), rec.Chain, rec.Seq, workload)
	return err
}
