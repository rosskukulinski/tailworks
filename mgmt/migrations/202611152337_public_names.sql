-- ADR-029, red-team H10: public-name ownership. A public workload is served
-- at https://<name>.<tailnet>/, so the name is unique per tailnet, not per
-- tenant. The first tenant to deploy public routes under a name on a
-- tailnet owns it, across undeploys, until it releases it
-- (DELETE /v1/tenants/{id}/public-names/{name}) or the tenant is deleted.
-- tailnet is the tenant's tailnet, lower-cased, without a trailing dot.
CREATE TABLE IF NOT EXISTS public_names (
  tailnet TEXT NOT NULL, name TEXT NOT NULL, tenant_id TEXT NOT NULL,
  claimed_at BIGINT NOT NULL, claimed_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (tailnet, name));
CREATE INDEX IF NOT EXISTS public_names_tenant ON public_names (tenant_id, claimed_at, name);
