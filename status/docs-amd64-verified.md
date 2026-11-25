Last update: 2026-11-22 PT. amd64 microVM tier verified on a nested KVM test host; docs, ADR-030 (d) and ledger H16/H24 updated.

## What was measured

On a nested KVM test host (an Ubuntu 24.04 amd64 guest, not bare metal), at
origin/master 45cd920, 2026-11-22:

- `TestSmoke`: passes. Boot 0.70 s, ready 0.92 s.
- `TestSnapshotRestore`: passes. On-demand restore p50 93 ms, p95 130 ms;
  base snapshot create 1.9 s.
- `make e2e-vm`: passes. That covers every `hostd/vm` test (Pool; Snapshot
  Restore, Pool, Uniqueness and Rotate; Smoke; Volume; DgramNIC), plus
  `TestE2EVMKV`, `TestE2EVMR2`, `TestE2EVM` 14/14 and `TestE2EVMSnapshot` 14/14.
- `TestSnapshotKernelState` (H24, #157): passes. The three restored copies
  share `net_secret` (the ISN key), `ts_secret`, the first ephemeral port,
  the KASLR `_text` (no slide: Firecracker boots `vmlinux` directly) and the
  TFO key. The cold-boot control differs on the ISN key, the ts key and the
  port, and has the same `_text`.
- The #151 base-snapshot path (`makeBase` with `Base: true`, then
  `snapshot-base`) and `helper.TestRT3_H16_BaseGuard` ran on real KVM and
  passed.
- Not run: a separately installed `tw-vmhelper` daemon, and
  `make e2e-container` on amd64.

## What changed (docs only)

- docs/COMPAT.md, docs/containers.md, docs/install-testing.md,
  docs/INSTALL.md: amd64 is no longer "never booted to ready"; they state what
  passed, where and when. Containers on amd64 stay "not verified".
- ADR-030 (d): the measured sharing replaces "hasn't yet been run". The verdict
  (shared, not exploitable across tenants) is unchanged.
- security/LEDGER.md: H24 and H16 evidence, and a run-log line.
- status/sec-157-snapshot-secrets.md and status/fix-151-vmhelper-base.md:
  their "not run on KVM" notes now point here.

## Tests

See the PR body.
