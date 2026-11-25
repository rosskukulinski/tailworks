Last update: 2026-11-21 12:00 PT. `make e2e-tw-upgrade`: single-machine `tw upgrade` end to end (issue #111); two `tw upgrade`/`tw up` bugs fixed.

# e2e-111-upgrade: single-machine `tw upgrade` end to end

## What's built

- `e2e/twupgrade` (build tag `e2etwupgrade`, `make e2e-tw-upgrade`): hermetic, no
  credentials. testcontrol plus a fake Tailscale API (OAuth exchange, keys, policy,
  devices, and GitHub's "latest release" for `tw version --check`). Release N is this
  tree stamped 0.0.1 and N+1 is this tree stamped `VERSION`. Both are real tarballs in
  release.sh's layout (bin/, share/tailworks/release.json, install.sh, VERSION,
  SHA256SUMS + SHA256SUMS.sig), signed with an Ed25519 key made for the run and pinned
  with `TW_RELEASE_SIGNERS`. workerd in the tarballs is a stub that answers
  `--version`, and `TW_WORKERD` is the real one. The flow, driven only through the
  installed `prefix/bin/tw`:
  1. N's install.sh installs into a prefix, checking the signature with ssh-keygen.
     `tw up` runs in the default single-process layout, and the state dir is the
     prefix.
  2. Deploy an app with a KV namespace and a D1 database, and write a key and two rows.
  3. Refusals: unsigned (`release_unsigned`), signed by an unpinned key
     (`release_bad_signature`), and install.sh changed after signing
     (`release_bad_signature`). Each refused release's install.sh would create a
     sentinel file, and none does. After each one, VERSION, the backups and the
     supervisor PID are unchanged.
  4. Two N+1 releases fail the health gate (`--health-timeout 20s`): one whose
     `tw up` exits 1, and one that runs N's binaries but claims N+1 in release.json,
     so mgmt reports the wrong version. Both are rolled back (`rolledBack: true`). The
     test asserts the backup with the store, VERSION = N and VERSION.prev = N+1, a new
     supervisor PID, installed and running version N (running version from
     `tw version --check` → mgmt `/v1/status`), the deployment active, and the KV and
     D1 data intact.
  5. A good N+1: `upgraded: true`, `health: ok`. The test asserts the backup
     (`<time>-0.0.1` with mgmt/mgmt.db and tw.json), VERSION = N+1 and
     VERSION.prev = N, a new supervisor PID, running version N+1, and the data.
  `tw upgrade --plan` is asserted to change nothing.
- `cmd/tw/upgrade_plan_test.go` and `testdata/upgrade-plan.golden`: a golden of
  `tw upgrade --plan` (text and `--json`) for a signed release with a breaking
  note, operator actions, a contract migration and a new workerd. Its versions
  are past every embedded catalog entry, so editing the catalog doesn't move it.
  Use `-update` to rewrite it.
- Wiring: a Makefile target (its own `.PHONY` line), a `go vet` line in `make lint`,
  a nightly matrix entry, a release.yml `gates` step, and a row in AGENTS.md's suite
  table.

## Bugs found and fixed (separate commits)

1. **`tw up` on a set-up state dir reset every flag that wasn't given.** Affected:
   host ID, hostname prefix, control and API URL, `--ephemeral`,
   `--allow-sibling-egress`, `--local-dev`, warm keys, pool nodes, heartbeat,
   tombstone and push port. Each fell back to its default, and the default was
   saved. `tw upgrade` restarts a non-service `tw up` with `tw up --yes`, so any
   upgrade or rollback came back with defaults. The suite's first run failed there:
   the rollback's restart lost `--tsapi-url`. On a `--local-dev` state the restart
   failed outright with `state_conflict`. Fix: `cmd/tw/up_saved.go`
   (`keepSavedUpFlags`, one call in `cmdUp`). A flag that is given still wins.
   `--workerd` is not kept, so an upgrade picks up the new install's.
   `TestUpKeepsSavedSettings` uses `tw up`'s real flag set, so a renamed flag fails
   it.
2. **`tw upgrade --plan` with tw not running** still listed "if it isn't: roll back
   …" after replacing the health-check step. No health check runs in that case, so
   nothing would be rolled back. The step is now dropped.

## Tests run (2026-11-21, macOS arm64)

- `make e2e-tw-upgrade`: PASS 4 times (one `go test` run at 140 s, then 110 s, 107 s and 114 s under `make`).
- `go test ./cmd/tw -run 'TestUpKeepsSavedSettings|TestUpgradePlanGolden'`: PASS.
- `make lint`: PASS.
- `make test`: PASS, apart from two failures in packages this branch doesn't touch:
  `mgmt` TestWarmKeyRefillRespectsThrottle and `hostd` TestContainerFenceInheritance.
  The machine was shared and under load. Both passed on a rerun
  (`go test -count=1 -run … ./mgmt/` and `./hostd/`).
- `make e2e-upgrade`: PASS (27 s).

## Gaps and next steps

- The rollback restarts tailworks with the *old* release's `tw up`. Upgrading from a
  published release older than this fix (v0.2.0) still resets non-default
  `tw up` flags when that release restarts. This tree can't fix that. The suite
  upgrades from this tree, so it doesn't cover it.
- Upgrading from the published v0.2.0 tarball (as `scripts/upgrade-from.sh` does for
  e2e-upgrade) isn't covered. That tarball is signed with the real key, so it would
  need the pinned signers for N and the run key for N+1. Its install.sh honours
  `TW_RELEASE_SIGNERS` too.
- `--service` installs (launchd/systemd restart) aren't covered. They need a real
  user service manager. e2e-linux covers `tw up --service`, not `tw upgrade`.
- A contract migration (no automatic rollback) isn't exercised end to end. It needs
  a tree with a `-- tw:contract` migration.
