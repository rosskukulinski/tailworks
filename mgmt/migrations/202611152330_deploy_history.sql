-- ADR-046: deployment history and bundle retention.
-- bundle_sha256 is recorded at deploy (and before a bundle is pruned), so
-- history keeps the hash after the bytes are gone; bundle_pruned_at is when
-- retention dropped the bundle (0 = kept); active_at is when the host first
-- reported the deployment active (what "previous good" means); rollback_of
-- names the deployment a rollback redeployed.
ALTER TABLE deployments ADD COLUMN bundle_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN bundle_pruned_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN active_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN rollback_of TEXT NOT NULL DEFAULT '';
