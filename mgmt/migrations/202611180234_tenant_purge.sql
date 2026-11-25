-- Issue #107: deleting a tenant removes everything it owns.
-- tenant_purges: a deleted tenant whose local state (tenants/<id>/, R2
-- blobs) a host has not removed yet; listed in the host's heartbeat reply
-- until the host reports it done (purgedTenants).
-- custody_names: every custody entry mgmt writes, by tenant, so a tenant's
-- entries can be enumerated (custody itself cannot list names) and
-- deleted with the tenant.
CREATE TABLE IF NOT EXISTS tenant_purges (
  host_id TEXT NOT NULL, tenant_id TEXT NOT NULL, created_at BIGINT NOT NULL,
  PRIMARY KEY (host_id, tenant_id));
CREATE TABLE IF NOT EXISTS custody_names (
  name TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS custody_names_tenant ON custody_names (tenant_id);
