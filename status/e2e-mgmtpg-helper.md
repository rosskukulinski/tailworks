Last update: 2026-11-24 23:05 PT. Shared Postgres harness pieces for e2e suites.

# e2e-mgmtpg-helper

Groundwork for issue #421 (and #415's "one mgmt replica of several"): the
data-plane chaos loop (`e2e/dataplane`) is getting an option to run mgmt as
2–3 `tw-mgmt` replicas on Postgres, as `e2e/multimgmt` does. `_test.go`
helpers can't be imported across packages, so the reusable parts of the
multimgmt harness move to a small non-test package both suites use.

## What changed

- New `e2e/internal/mgmtpg`:
  - `Start` / `Postgres`: the throwaway `postgres:17-alpine` container (name,
    port or a free one, docker context, `postgres.log` on cleanup), `Restart`,
    `Dump` (pg_dump, for a "no secret value in the store" check).
  - (The per-replica fault proxy is `e2e/chaoskit`'s, which landed meanwhile.)
  - `FakeSetec`: the shared custody (put, activate, get, delete).
  - `ViaProxy`, `Host`, `WaitReady`.
- `e2e/multimgmt` uses them through thin wrappers (`startPG`,
  `newFakeSetec`, `setecDelete*`), so none of its tests changed.

## Tests

- `make lint`: pass.
- `make e2e-multimgmt` (`TW_MM_SEED=1`, own container name and port):
  pass, every scenario and H-regression (1270 s).
