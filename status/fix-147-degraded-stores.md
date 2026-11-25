Last update: 2026-11-21 PT. #147: mgmt records, shows and audits a degraded platform store; `tw d1 repair --skip <seq>`.

# fix-147-degraded-stores

Issue: #147, follow-up to #106 (`status/fix-106-d1-replay.md`). #106 made a
store object stop its replay at a journal entry it can't apply and refuse
writes (`store_degraded`); mgmt didn't know.

## Design

- **Report: a store-status call, not the heartbeat.** The Store object
  (`hostd/gen/js/store.js` `#report`) calls hostd's `POST
  /v1/store/status` once per change (it keeps the seq it last reported in
  `__tw_store_reported`), when a catch-up or a commit stops at an entry and
  when it heals. hostd adds tenant, workload and lease epoch
  (`hostd/storestatus.go`) and calls mgmt's new `POST
  …/stores/{kind}/{name}/status` (`ReportStoreStatus`, roles `admin|host`,
  fenced like the journal). The heartbeat carries no per-store state, and
  the object already talks to mgmt through this channel. A failed report
  is retried by the next sync that ends the same way.
- **Record:** five columns on `stores` (migration
  `202611211922_store_degraded`): seq, error (≤ 1000 chars), host, time and
  the reporting workload (or replica). `StoreInfo.degraded` /
  `Store.degraded` in the API; `StoreJournal.degraded` too, so a store
  object on a new host that replays past the entry clears a stale record.
- **Clearing rules** (`mgmt/storedegraded.go`): a healthy report clears
  the record if it is from the host and workload that made it, or if it
  has applied the entry and the recording host no longer holds that
  workload's lease (a move). A replica that applied the entry fine does
  not clear another replica's record. Reports for another incarnation are
  ignored (204).
- **Audit:** `store_replay_failed` on the first report of an entry from a
  host/workload (repeats only update the record). `store_repair` for a
  repair. Both added one per line in `AuditKind`.
- **Repair:** `POST …/stores/{kind}/{name}/repair {skip}` (roles
  `admin|tenant:data`) deletes journal entry `skip`, only if mgmt records
  the store as degraded at exactly that seq (`409 store_not_degraded`
  otherwise, a new error code), fixes entries/bytes, and clears the record.
  Every host's replay then reads from the entry before it to the one after
  it. The object retries on its next write (or a read once its freshness
  lapses: 1 s for D1). `store.js` also clears its degraded mark when the
  skipped entry was the last one (seq jumps to head with nothing to apply).
- **CLI:** `tw d1 repair <db> --skip <seq> [--yes]` (`cmd/tw/d1repair.go`,
  in `destructiveCommands`: asks for the name on a terminal, refuses
  without `--yes` otherwise). No KV/R2 repair command: their entries only
  fail to apply when a newer tailworks wrote them or one is corrupt, so the
  fix is a hostd upgrade; the REST route works for any kind.
- **Display:** a `✗ degraded: journal entry N failed to apply on host H …`
  line plus a fix line under the row in `tw d1 list`, `tw kv namespace
  ls`, `tw r2 bucket ls` (`degraded` in `--json`); `tw status` (local and
  linked) lists every degraded store from the new `GET
  /v1/stores/degraded` (admin); the console overview has a "Degraded
  stores" section (operators, only when there are any).

## Changed

- `api/openapi.yaml` (+ `make api`): `ReportStoreStatus`, `RepairStore`,
  `ListDegradedStores`; `StoreDegraded`, `StoreStatusReport`,
  `StoreRepair`, `DegradedStores`; `Store.degraded`,
  `StoreJournal.degraded`; `AuditKind` `store_replay_failed`,
  `store_repair`; `ErrorCode` `store_not_degraded`.
- mgmt: `storedegraded.go`, `sqlstore_storedegraded.go` (new);
  `stores.go` (field + page field), `sqlstore_stores.go` (columns, scan).
- hostd: `storestatus.go`, `mgmtapi/storestatus.go`,
  `fakemgmt/storedegraded.go` (new); `stores.go` (one case, two fields),
  `gen/js/store.js` (`#report`), fakemgmt route registration and field.
- CLI: `d1repair.go`, `storedegraded.go` (new); `d1.go`, `kv.go`, `r2.go`,
  `up.go` (status), `confirm.go`.
- Console: `mgmt/ui/stores.go` (new), `ui.go`, `templates/overview.html`.
- Docs: `docs/d1.md` "Recovery" (report, skip or rebuild), `docs/errors.md`
  (`store_degraded` fix, `store_not_degraded`), ADR-044 §3, `docs/cli.md`
  (`make cli-docs`), `changes/fix-147-degraded-stores.md`
  (`schema-expand`, `protocol`).

## Tests

New: `mgmt TestStoresDegraded` (report, repeat, listing, journal page,
degraded list, audit once, heal, wrong-entry repair, repair, audit),
`mgmt TestStoresDegradedMove` (a new holder's healthy report clears the old
host's record only once it applied the entry), `hostd TestD1/Repair` (the
real store object reports after `ReplayFailureStops`, the repair skips the
entry, the next write applies the entry after it and the new one, the
record is gone), `cmd/tw TestCLID1Repair`, `TestDestructiveCommandsConfirm`
(repair), `mgmt/ui TestPagesRenderPopulated` (the console section).

Commands and results (2026-11-21):

- `make lint`: pass (the `vulncheck` step reports stdlib findings and is
  ignored by the target, as on master).
- `make test`: everything passed except `netd TestEgressWaitsForNewPeerPath`
  (known macOS flake #139; 3/3 on rerun with `-race -count=3`) and `mgmt
  TestTenantRolesConsistent`. That one was real: the new host-only status
  route had to be listed in `tenantOperatorRoutes`. Fixed, then `go test
  -race ./mgmt/...` passed.
- `make test-pg` (against my own throwaway postgres:17 container through
  `TW_TEST_POSTGRES_DSN`, because the script's fixed container name is
  shared with other worktrees): pass, including `TestStoresDegraded*`.
- `make e2e-placement`: pass (`TestD1SurvivesRestartAndMove`,
  `TestR2MoveBetweenHosts`, `TestSmartPlacementHermetic`).
- `make e2e-upgrade` (`changes-check` flags protocol and schema-expand):
  pass.
- `make changes-check`: ok (`changes/fix-147-degraded-stores.md`).
