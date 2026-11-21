Last update: 2026-11-17 PT. #106 fixed: a store never skips a journal entry it can't apply, and never claims to be synced past one.

# fix-106-d1-replay

Issue: #106, "D1 journal replay silently skips entries that fail and
marks the database synced".

## Root cause

`hostd/gen/js/store-d1.js` `apply()` ran each entry in a nested
transaction, caught any SQL error (and any entry that didn't decode),
logged `… failed to apply and was skipped` and returned normally. The
Store's catch-up (`hostd/gen/js/store.js`) then set `seq` to the last
entry and `synced=1`, so a database rebuilt on a new host could differ from
the one whose writes were acknowledged, with nothing durable to say so.
KV, R2 and the probe kind already threw from `apply` (the whole page
rolled back and the sync failed with an untyped error); only D1 skipped.

## Decision

Stop, don't skip, don't quarantine. In the Store substrate, for every kind:

- Catch-up applies each entry in its own nested transaction and moves
  `seq` past each one that applied. At the first one that throws it stops:
  `seq` stays before it, `synced` is not set, and the failing seq, error
  and time are kept in the object's own state (`__tw_store_degraded`).
- A degraded store serves reads from what it applied (an exact prefix of
  the journal), refuses writes with the new typed error `store_degraded`
  (409 from hostd and mgmt; `D1_ERROR: [store_degraded] …` in a Worker),
  and tries the entry again on later syncs (reads at the kind's
  freshness pace, every write and explicit sync). It heals on its own if
  the entry applies; deleting/recreating the store clears it.
- The commit path (append, then apply) uses the same code: if the apply
  of a just-journaled write fails, the store is degraded and the caller
  gets `store_degraded` saying the write is in the journal.
- D1's `apply` now throws instead of skipping. `tw d1 export` of a
  degraded database exports what applied, headed by
  `-- tailworks: degraded export: journal entry N failed …`; docs/d1.md
  "Recovery" describes export, delete, create, `execute --file`.

## Changed

- `hostd/gen/js/store.js`: `#applyEntries`, `degraded`, `#degradedError`;
  catch-up, `fresh` and `commit` use them; `#wipe` clears the state.
- `hostd/gen/js/store-d1.js`: `apply` throws; `export` tolerates
  `store_degraded`.
- `hostd/stores.go`, `mgmt/d1.go`: map `store_degraded` to 409.
- `api/openapi.yaml` (+ `make api`): `store_degraded` in the error table
  and `ErrorCode` enum; QueryD1/ExportD1 descriptions.
- Docs: `docs/errors.md` (`store_degraded`), `docs/d1.md` (Recovery),
  ADR-038 §1 (replay no longer skips), ADR-044 §3.
- Tests: `TestD1/ReplayFailureStops` (a failing D1 entry plus a good one
  after it, appended through the admin journal API: the bookmark stays at
  the entry before, reads serve, worker and operator writes fail with
  `store_degraded`, the export is headed and excludes later rows; the
  following `Delete` subtest shows recovery), and a probe-kind check in
  `TestPlatformStore` (a corrupt entry: `all` excludes everything from it
  on, `put` fails with `[store_degraded]`). Both fail on origin/master.
  Helper in `hostd/storedegraded_test.go`.

## Tests

See the PR's Tests section for the commands and results.

## Not done (follow-up)

The issue also asks for a durable, mgmt-side record: an audit kind
`store_replay_failed`, a `degraded` field on `StoreInfo` shown by
`tw d1 info` / `tw kv namespace ls` / `tw r2 bucket ls` / `tw status`
and the console, and `tw d1 repair --skip <seq>`. Those need a new
hostd → mgmt report call, a store column (migration) and CLI work; they
are tracked in #147. Today the degraded state is visible in
the error every write returns, the workload's log (`tw logs`,
`console.error` from the platform) and the export header.
