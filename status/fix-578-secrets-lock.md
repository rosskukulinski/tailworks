Last update: 2026-11-24 23:30 PT. Secret writes serialised across mgmt replicas (#578).

# fix-578-secrets-lock

## The bug

Found by the e2e/dataplane deploys loop (#421, `deploys-hosts` seed 1, V4):
a `DELETE SEC_C` through replica r1 and a `PUT SEC_B` through r2, 1 ms
apart, both got 2xx, and the next deployment was released SEC_C again. A
workload's secrets are one custody entry changed by read-modify-write, and
`secretsMu` only serialised that within one process.

## The fix

- `SQLStore.LockName` (`mgmt/sqlstore_namelock.go`): on Postgres a
  session-level advisory lock on a connection of its own (freed by
  Postgres if the replica dies); on SQLite a process mutex per key.
- `PutSecret` and `DeleteSecret` take it for the workload
  (`lockSecrets`); a store without it keeps the old process mutex.

## Tests

- `TestMM_H24_SecretWritesAcrossReplicas` (`e2e/multimgmt`, 2 replicas, a
  setec that answers reads 300 ms late so the two writes always overlap):
  without the fix every round lost an acknowledged put (10 missing-secret
  checks in 4 rounds); with it, none. Ledger row H24.
- `TestStoreLockName`: exclusion on the same key, no blocking across keys.
- `make test-pg`: pass (TestStoreLockName on SQLite and Postgres).
- `make lint`: pass. `make changes-check`: ok.
