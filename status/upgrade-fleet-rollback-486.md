# upgrade-fleet-rollback-486

Issue #486 (simplify audit E1: `operate-upgrade-nest-fleet`,
`operate-release-into-upgrade`, `lifecycle-upgrade-rollback-cmd`).

## Done

- `tw upgrade [<version>]` upgrades this machine: `--from`, `--plan`, `--yes`,
  `--allow-unsigned`; `--prefix`, `--health-timeout`, `--no-rollback` in the
  Advanced group. On a linked machine it still goes to the fleet and refuses
  the local-only flags (`bad_request` naming `tw upgrade fleet`); fleet-only
  flags on a local upgrade are refused the same way.
- `tw upgrade fleet [<version>]` and `tw upgrade fleet status|pause|resume|abort`
  (`cmd/tw/upgrade_fleet.go`).
- `tw upgrade import <tarball>…|<version> [--platform …]` and
  `tw upgrade releases` (`cmd/tw/upgrade_import.go`). A version downloads
  SHA256SUMS + SHA256SUMS.sig, verifies the signature with the pinned keys
  (no bypass), picks the self-upgrading hosts' platforms from `GET /v1/hosts`
  (this machine's platform when there are none, for the catalog entry),
  checks each tarball with `checkSum` and imports through the existing
  POST/PUT.
- `tw upgrade rollback [<backup-dir>] [--restore] [--prefix] [--yes]`
  (`cmd/tw/upgrade_rollback.go`): refuses linked machines
  (`remote_unsupported`) and npm installs (`npm_install`); needs
  bin.prev/share.prev/VERSION.prev; refuses when VERSION.prev is newer
  (already rolled back). Binary-only when `CheckSchema`'s MinBinary allows
  the previous release, else the newest `<state>/backups/<time>-<prev>` with
  restore + fence (hostd too for a `tw backup` with hostd data). Keeps
  `<time>-before-rollback` first. `confirmDestroy` (type `rollback`) for the
  restore path, in `destructiveCommands`; Postgres prints pg_restore +
  restore-fence. Restarts the way it ran and health-checks.
- Hidden aliases for one release, with a stderr note: `--fleet`, top-level
  `status|pause|resume|abort`, `tw release ls|import`.
- Hints and docs: upgrade.go, mgmt/upgrades.go, mgmt/v1_upgrades.go, admin UI
  host page, install.sh, RELEASING.md, INSTALL.md, errors.md, upgrading.md
  ("Roll back" replaces "Roll back by hand"), ADR-041 (dated note), openapi
  description (`make api`), `schema/cli.schema.json` (`upgrade releases`,
  `upgrade import`, new `upgrade rollback`), `docs/cli.md`.
- e2e/twupgrade step 7: `tw upgrade rollback --yes` back to N with the data.
- `changes/upgrade-fleet-rollback-486.md` (breaking; cli-json, protocol,
  config-default).

## Tests run

- `make lint`: ok.
- `make test` (on mbp via mbp-test.sh): ok.
- `make e2e-tw-upgrade`: ok (rollback 0.5.0 → 0.0.1 in 5s, data intact).
- `make e2e-upgrade`: ok.
- `make install-test`: ok (install.sh messages changed).
- New unit tests: `TestUpgradeSubcommand`, `TestUpgradeRollbackBinaryOnly`,
  `TestUpgradeRollbackRestore`, `TestUpgradeImportVerifySums`.

## Gaps

- `tw upgrade import <version>` against a real GitHub release isn't covered
  by a test (signature check and platform pick are unit-tested; the POST/PUT
  is the existing `releaseImport`).
- The Postgres rollback path is not exercised end to end (it prints the
  pg_restore / restore-fence lines like `tw restore`).
