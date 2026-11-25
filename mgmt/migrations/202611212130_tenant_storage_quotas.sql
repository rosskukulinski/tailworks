-- Issue #120: per-tenant storage quotas (mgmt/quota.go). 0 = unlimited,
-- which is what every existing tenant gets: the per-store and per-queue
-- limits (1 GiB each) still apply.
ALTER TABLE tenants ADD COLUMN max_store_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tenants ADD COLUMN max_queue_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tenants ADD COLUMN max_stores BIGINT NOT NULL DEFAULT 0;
