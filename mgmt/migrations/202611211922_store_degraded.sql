-- Issue #147: a store whose object stopped its replay at a journal entry it
-- can't apply (store_degraded), as its host last reported it. degraded_seq
-- 0 = not degraded (journal seqs start at 1).
ALTER TABLE stores ADD COLUMN degraded_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE stores ADD COLUMN degraded_error TEXT NOT NULL DEFAULT '';
ALTER TABLE stores ADD COLUMN degraded_host TEXT NOT NULL DEFAULT '';
ALTER TABLE stores ADD COLUMN degraded_at BIGINT NOT NULL DEFAULT 0;
-- degraded_workload: the workload (or replica) whose lease the reporting
-- host held, so another host's healthy report clears the record only once
-- that host no longer runs it (a move), not while it still does (a replica).
ALTER TABLE stores ADD COLUMN degraded_workload TEXT NOT NULL DEFAULT '';
