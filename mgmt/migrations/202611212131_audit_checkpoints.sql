-- Issue #120: audit retention (mgmt/retention.go). When the pruner deletes
-- a chain's oldest records it keeps the last deleted record's seq and hash
-- here, so the records that remain still verify: the first one must link
-- to this hash. pruned counts every record deleted so far.
CREATE TABLE IF NOT EXISTS audit_checkpoints (
  chain TEXT PRIMARY KEY, seq BIGINT NOT NULL, hash TEXT NOT NULL,
  pruned BIGINT NOT NULL DEFAULT 0, at BIGINT NOT NULL DEFAULT 0);
