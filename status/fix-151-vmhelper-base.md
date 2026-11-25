Last update: 2026-11-21 21:40 PT. vmhelper snapshot-base guard (#151, ledger H16 / F8).

## Follow-up, 2026-11-22: the KVM path has run

The gap noted under Tests below is closed. On a nested KVM test host
(Linux amd64), the base-snapshot path (`makeBase` with `Base: true`, then
`snapshot-base`) and `helper.TestRT3_H16_BaseGuard` ran on real KVM and
passed, along with `TestSnapshotRestore` and the rest of `make e2e-vm`.
Details: `status/docs-amd64-verified.md`.

## What changed

- `helperapi.StartRequest.Base`: hostd sets it on the base boot only
  (`bootOpts.base`, from `makeBase`). The helper stores it on the session
  (`session.forBase`).
- `hostd/vm/helper/baseguard_linux.go`: `checkBaseStart` (Base needs a
  volume slot, no image, no restore), `canSnapshotBase` (Base, volume slot,
  no volume, not a restored copy), `canAttach` (refused on a Base session).
- `snapshotBase` checks before it defers `stop()`: a refused target is left
  running instead of being killed.

## Upgrade interop

The helper protocol stays at 2 and the field is additive.

- New hostd, old helper: the old helper's JSON decode ignores `base`, and it
  keeps its old check. Snapshots work as before.
- Old hostd, new helper: the old hostd never sets `Base`, so the new helper
  refuses its `snapshot-base`. hostd logs "microVM base snapshot failed;
  booting instead of restoring" and boots VMs cold, retrying the base every
  minute, until hostd is upgraded. Only hosts with snapshots on (off by
  default) see this. In ADR-041's bounce the helper goes first and hostd
  restarts right after, so the window is short.

## Tests

- `GOOS=linux go vet ./hostd/vm/...`, `GOOS=linux go test -c ./hostd/vm/helper`: ok.
- `helper.TestRT3_H16_BaseGuard` on a Linux amd64 test host (no root): passes.
  The same test built against the old guard conditions fails: the claimed
  pool VM and the restored copy reach the VMM's pause call and are stopped,
  attach on a base session isn't refused, and `start` accepts the bad
  `Base` combinations.
- Whole `hostd/vm/helper` package on the same host: PASS.
- `make lint`: ok. `make changes-check`: ok (fragment added anyway, `protocol`).
- `make test` on the mbp runner: ok (exit 0).
- `make e2e-vm` / the KVM snapshot smoke test (`TestSnapshotRestore`): not
  run to completion. On that host I built a current guest image and ran
  `TestSnapshotRestore`, and also the cold-boot `TestSmoke` as a control,
  with the in-process helper. Both failed the same way before reaching any
  changed code: the guest kernel soft-locks during boot ("microVM not pooled
  after 30s") under nested KVM. The control shows this is the test host,
  not this change. The real base-boot path (`makeBase` → `Base: true` →
  `snapshot-base`) is still untested on KVM.
