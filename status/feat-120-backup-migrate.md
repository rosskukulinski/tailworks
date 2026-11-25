Last update: 2026-11-21 23:00 PT. Issue #120, first half: online backup and restore, SQLite to Postgres, tw up store flags, docs.

# feat-120-backup-migrate

Refs #120. Per-tenant quotas, `tw doctor` disk and store checks, and
retention are the other half of the issue, done on another branch.

## Built

- **`tw backup [--out <dir>]`** (`cmd/tw/backup.go`). It works while
  tailworks runs and reuses `tw upgrade`'s backup (`backupMgmtTo`): the
  store is copied with `VACUUM INTO`, then custody, the ID secret, mgmt's
  node state and `tw.json`. It writes `backup.json` (version, store
  kind, custody kind, schema status, and what is not included: `hostd/`,
  `mgmt/registry/`, the OAuth client file, a Postgres store). It refuses
  an `--out` inside `<state>/mgmt` or a non-empty one.
- **`tw restore <dir> [--yes]`**. Destructive, so it is guarded by
  `confirmDestroy` and listed in `destructiveCommands`. It refuses while
  tailworks runs (`already_running`), copies the current state to
  `<state>/backups/<time>-before-restore`, puts the backup back
  (`restoreMgmt`), then runs `mgmt.RestoreFenceDSN`. It also takes `tw
  upgrade` backups, which have no `backup.json`.
- **`tw-mgmt migrate --from sqlite --to <dsn> | --to-file <file>`**
  (`mgmt/storecopy.go`, `cmd/tw-mgmt/maintenance.go`):
  `mgmt.CopySQLiteStore`.
  - Refuses a source a replica is live on, and a non-empty target.
  - Copies a `VACUUM INTO` snapshot, brought to this binary's schema,
    contract migrations included. The source is never written.
  - Copies every table in one target transaction, generically: columns
    come from `sqlite_master`/`pragma_table_info` and
    `information_schema`, and values are converted by the target column
    type.
  - Copies `schema_meta` (restore generation) too, then runs
    `RestoreFence` on the target.
  - Works for SQLite targets too, so CI covers it without Postgres.
- **`tw up --db <dsn> | --db-file <file>`, `--custody setec --setec-url
  <url>`** (`cmd/tw/up_store.go`). They pass through to the embedded mgmt
  (`mgmtd.Options`) and to `tw-mgmt` in the separate layout, are saved in
  `tw.json` and kept by `keepSavedUpFlags`.
  - Refuses `--db-file` on an install with a SQLite store when the
    Postgres store was never migrated (`state_conflict`, with the
    migrate command as the fix).
  - Refuses to change custody under an install that has secrets.
  - `tw upgrade` now reads the schema from the configured store, and its
    backup skips a stale `mgmt.db` on a Postgres install.
- **Store objects whose copy is ahead of the journal** (`hostd/gen/js/store.js`).
  After a restore (or an upgrade's rollback), a host's store object could
  hold entries past the restored journal head with the same incarnation.
  It served the undone writes and every write failed with
  `store_conflict` (8 retries, then an error). It now wipes and replays
  from the journal.
- **Docs.**
  - New `docs/postgres.md`: when to switch, the move, backups with
    Postgres, HA replicas.
  - `docs/INSTALL.md` "Back up and restore" rewritten (`tw backup`,
    `tw restore`, the offline tar for `hostd/`), plus a "When to switch
    to Postgres" paragraph and a link from the scale-out section.
  - Links from services.md, upgrading.md and locks.md.
  - errors.md: `backup_failed`, `restore_failed`; `already_running` and
    `remote_unsupported` widened.
  - docs/README.md, llms.txt, cli.md.

## Decisions

- **HA is not a `tw up` pass-through.** `tw up` runs one replica,
  embedded with this machine's host, and its node is named `tw-mgmt`,
  which is the Service label hosts would use. So `--db`/`--custody` give
  a single mgmt on Postgres and setec, and HA (several replicas behind
  `svc:tw-mgmt`, shared `-registry-dir`) is documented as running
  `tw-mgmt` directly. No `--registry-dir` flag on `tw up`: one replica
  needs no shared registry.
- **The restore fence in migrate** runs on the target after the copy, as
  the issue asked.

## Tests

- `TestMigrateStoreCopy` (mgmt). The source holds a KV journal with a
  compacted key, queue messages, a secret (custody_names), the audit
  chain, a deployment and a lease. The test:
  - refuses the copy while the replica is live;
  - copies, then checks every row of every table is equal (epochs minus
    the fence);
  - opens a Mgmt on the target with the same custody: the journal reads
    and unseals equal to the source, `PeekQueue` is equal, `VerifyAll` is
    ok, and the secret is listed;
  - refuses a second copy into the now non-empty target.
  It runs on SQLite always and on Postgres under `make test-pg`.
- `TestBackupRestore` (cmd/tw): a backup while a goroutine keeps writing
  leases. The backup is a prefix of the writes. After changes and a
  restore: the changes are gone, the lease epoch is 1+2²⁰, the restore
  generation is 1, custody holds the old value, `tw.json` is the old one,
  and the safety backup holds the replaced state.
- `TestCLISingleProcess` (hermetic full stack) now also runs
  `backupRestoreRunning`: `tw backup` on the running install, `tw
  restore` refused while running, then `tw down`, `tw restore --yes`,
  `tw up`, and the same tenant and deployments in `tw ls`.
- `TestPlatformStoreJournalRewound` (hostd, real workerd): rewinds the
  fake mgmt's journal to an earlier export. Reads serve the journal's
  state and the next write gets seq head+1. It fails without the
  store.js change: `all: map[a:1-lost b:2 c:lost]`.
- `TestUpStoreFlags` (cmd/tw): validation, the tw-mgmt args and embedded
  options, kept flags, and the custody-switch refusals.

Results (2026-11-21, local; both mbp slots were busy):

- `make lint`: ok (after merging origin/master).
- `make test`: ok, every package.
- `make test-pg` (OrbStack Postgres 17): ok, including
  `TestMigrateStoreCopy/postgres`.
- `make e2e-tw-upgrade`: ok (`TestTwUpgradeSingleMachine`, which rolls
  back with KV and D1 data through the changed backup/restore code).
- `make e2e-upgrade`: ok (`TestUpgradePreviousToCandidate`).
- `make cli-docs`: docs/cli.md regenerated.

## Gaps and next steps

- **Moving file custody to setec isn't supported.** File custody names are
  hashed on disk, and only some are indexed in `custody_names`. An
  install that started on file custody can move its store to Postgres
  but can't become a replica set without a custody migration tool. The
  docs say to start a would-be HA install with `--custody setec`.
- `tw backup` doesn't copy `hostd/` (DO storage, `fs` R2 bytes). These
  are written by running workloads, so the docs give the offline copy.
- The `tw up` refusal of an unmigrated Postgres target is unit-tested
  only for its validation paths. The check itself (`CheckSchema`
  Applied == "") needs a Postgres the cmd/tw tests don't start.
