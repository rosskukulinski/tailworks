Last update: 2026-11-24 23:35 PT. A racing history read no longer erases a rollback's rollbackOf (#563).

# fix-563-rollbackof-clobber

## The bug

Found by the e2e/dataplane deploys loop (#421, `deploys-mixed` seed 1, V5):
an acknowledged rollback (`rollbackOf` in the 202 and in its audit record)
was listed in the history without `rollbackOf` for the rest of the run.
`Deploy` commits the record first and `afterDeploy` writes the hash and
`rollback_of` after. A history read in between (`GET …/deployments`, or
another rollback's `rollbackTarget`) backfills the missing hash in
`fillRecord` with `SetDeploymentMeta(id, hash, <rollbackOf it read: "">)`;
when that write lands last, `rollback_of` is gone for good.

## The fix

`SetDeploymentMeta` never overwrites a recorded hash or `rollback_of` with
an empty value. The record can still be listed without `rollbackOf` for
the moment between its commit and `afterDeploy`; writing both in
`CreateDeployment`'s transaction would close that too, at the cost of a
store-interface change.

## Tests

- `TestStoreSetDeploymentMetaKeepsRollbackOf` (SQLite and Postgres): the
  racing order; fails before the fix.
- `make test-pg`: pass (the new test on both stores).
- `make lint`: pass. `make changes-check`: ok.
- The deploys loop with the fix: V5 clean on every run since.
