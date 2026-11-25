Last update: 2026-11-24 17:30 PT. Python Workers run on the microVM tier: the guest image carries the pinned Pyodide bundles (follow-up to status/python-support-292.md and status/python-cron-queues-assets.md).

# python-microvm

ADR-055 amended with decision 9.

## What changed

- `internal/pyodide/bundles.txt`: the pins (file, sha256, size) as data,
  embedded by the Go package and read by `guest/build-image.sh`; one source.
- `guest/build-image.sh` + `guest/inputs.sh` (`PYODIDE_URL`,
  `PYODIDE_LIST`, `PYODIDE_GUEST_DIR`): each bundle fetched with the
  sha256-checked `fetch`, installed 0444 in `/usr/share/tailworks/pyodide`,
  listed in `manifest.json` "pyodide". Image: 263 MB (arm64).
- hostd: on the microVM tier, no host-side fetch; the guest workerd gets
  `--pyodide-bundle-disk-cache-dir /usr/share/tailworks/pyodide` in the
  load's `WorkerdArgs` (warm pool and snapshot base unchanged). The
  `tier_unavailable` refusal for Python (hostd and bundle parse) is gone.
- `e2e/vm/python_test.go`: TestE2EVMPython (default pool) and
  TestE2EVMPythonSnapshot (pool off, every VM restored): Worker + Python DO,
  verified caller, DO state across a hostd restart (a new VM), then a
  redeploy at compatibility_date 2026-11-01 running Python 3.14 (another
  bundle).

## Measured

- workerd RSS with one Python Worker + one DO: ~250 MiB (JS-only ~29 MiB),
  macOS arm64. Fits the default 512 MiB guest.
- vz: deploy → active ~3.7-4.0 s, first request ~50-63 ms.
- Firecracker (Lima twd-dev, nested): deploy → active ~2.7 s (pool and
  restore), first request ~52-67 ms.

## Tests run

- `TW_DEV_VM=twd-dev make e2e-vz` (whole suite, new image): pass.
- `make e2e-vm TW_VM_KERNEL=~/fc/vmlinux` in Lima twd-dev (Firecracker,
  whole suite incl. snapshots, guest image rebuilt there): pass.
- `e2evm.test -test.run TestE2EVMPython` in twd-dev (both variants): pass.
- `go test ./internal/pyodide ./hostd/bundle`, `go test ./hostd -run TestPython`: pass.
- `make lint`, `make test`: see the PR.

## Gaps

amd64 guest not run (nas-1 twd-amd64 not tried); Python Workflows; Python
DOs with their own node; memory snapshots.
