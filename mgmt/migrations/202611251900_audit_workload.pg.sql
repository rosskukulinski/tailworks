-- Issue #512: GET /v1/audit?workload=. The workload an audit record is
-- about, derived when the record is stored (sqlstore_auditworkload.go):
-- its object's workload, else the workload a mgmt record names in its
-- detail (deploy, undeploy, secrets); never from host or tenant-written
-- detail. A side table, not an audit column: audit rows are append-only
-- (no UPDATE to backfill), and this is not part of the hashed record.
-- Rows exist only for records with a workload; retention prunes them with
-- their records.
CREATE TABLE IF NOT EXISTS audit_workloads (
  chain TEXT NOT NULL, seq BIGINT NOT NULL, workload TEXT NOT NULL, PRIMARY KEY (chain, seq));
CREATE INDEX IF NOT EXISTS audit_workloads_workload ON audit_workloads (workload);
INSERT INTO audit_workloads (chain, seq, workload)
  SELECT a.chain, a.seq, o.workload FROM audit a JOIN objects o ON o.id = a.object_id
  WHERE a.object_id <> '' AND o.workload <> '';
INSERT INTO audit_workloads (chain, seq, workload)
  SELECT chain, seq, w FROM (
    SELECT chain, seq, CASE WHEN raw LIKE '{%}' THEN
      CASE WHEN jsonb_typeof(raw::jsonb #> '{detail,workload}') = 'string' THEN raw::jsonb #>> '{detail,workload}' END
    END AS w
    FROM audit a WHERE chain = 'mgmt' AND NOT EXISTS (SELECT 1 FROM objects o WHERE o.id = a.object_id)) d
  WHERE w IS NOT NULL AND w <> '';
