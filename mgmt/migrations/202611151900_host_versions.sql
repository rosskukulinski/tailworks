-- ADR-041 §7: hosts and replicas report their version,
-- protocols and features; a cordoned host takes no new placements (§5).
ALTER TABLE hosts ADD COLUMN version TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN os TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN protocols TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN features TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN cordoned_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN cordoned_by TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN version TEXT NOT NULL DEFAULT '';
