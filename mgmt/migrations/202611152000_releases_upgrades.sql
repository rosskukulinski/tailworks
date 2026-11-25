-- ADR-041 §8: imported release catalog entries (the
-- built-in ones are embedded in the binary) and durable upgrade runs.
CREATE TABLE IF NOT EXISTS releases (
  version TEXT PRIMARY KEY, entry TEXT NOT NULL,
  imported_at BIGINT NOT NULL, imported_by TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS upgrade_runs (
  id TEXT PRIMARY KEY, to_version TEXT NOT NULL, state TEXT NOT NULL, run TEXT NOT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS upgrade_runs_created ON upgrade_runs (created_at, id);
