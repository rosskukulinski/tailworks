-- ADR-043 D4, D5: several tenants on one tailnet, one per team.
-- owner_tag is the tag that owns the tenant's tags and that its OAuth
-- client is scoped to ('' = tag:twd, as before). name_prefix goes in
-- front of every tailnet name twd generates for the tenant ('' = none).
ALTER TABLE tenants ADD COLUMN owner_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN name_prefix TEXT NOT NULL DEFAULT '';
