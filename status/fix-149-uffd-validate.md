Last update: 2026-11-21 PT. Fix #149 (ledger H11 / F8): the vmhelper validates the uffd region table.

# fix-149-uffd-validate

## Problem

The root vmhelper serves userfaultfd page faults for every restored microVM
on a Linux host and takes the guest memory region table from the jailed
Firecracker process. It checked only that `PageSize` was a power of two and
that `Offset+Size <= len(mem)` (which can wrap). A crafted table (wrapped
`Offset`, unaligned `Base`) made `copyPage` index the memory file out of
range and panic the whole helper, dropping every VM on the host
(security/LOOP.md invariant I8).

## Fix

- `hostd/vm/helper/uffdregions.go` (new, no build tag): `uffdRegion`,
  `parseRegions` (decode + validate), `validateRegions`, `pageSource`.
  Checks: 1 to 64 regions; page size a power of two in [4 KiB, 1 GiB];
  `Base % PageSize == 0`; `Size > 0 && Size % PageSize == 0`; `Base+Size`
  does not wrap; `Offset % 4096 == 0` (UFFDIO_COPY source alignment);
  `Offset <= len(mem) && Size <= len(mem)-Offset`; no overlap in guest
  memory. Two regions may name the same file range (harmless: both copy
  the same base bytes).
- `uffd_linux.go`: `serve` uses `parseRegions`; `copyPage` uses
  `pageSource` and re-checks the bound before `&mem[src]`; `serve` and
  `prefetch` defer `recoverSession` (a panic becomes that VM's uffd error).
- `recover_linux.go` (new) + one line in `serveConn`: a panic while serving
  one client connection is logged with its stack, the connection's VM is
  stopped by the existing deferred cleanups, and the helper keeps serving.

## Tests

- `go test -count=1 -run 'TestRT3_H11|TestUffd' -v ./hostd/vm/helper`
  (macOS): `TestRT3_H11_UffdRegionValidation` (33 cases),
  `TestUffdPageSource` pass.
- `go test -run XXX -fuzz FuzzRegionTable -fuzztime 60s ./hostd/vm/helper`:
  21.6M execs, no failures.
- Linux (amd64 KVM test VM, test binary cross-compiled):
  `helper.test -test.run 'TestRT3_H11|TestUffd'`: all pass, including
  `TestRT3_H11_UffdBadTableFailsSession` (fake VMM, a pipe as the
  userfaultfd, five crafted tables each fail their session, then a good
  table is served up to `UFFDIO_COPY`) and `TestUffdRecoverSession`.
- Before/after: the same test built with the old validation (overlay) fails
  for "wrapped offset", "offset wraps" and "unaligned base": the old check
  accepts them.
- `make lint` and `make test` (macOS, after merging origin/master): pass.
- `make e2e-vm` on an amd64 KVM test VM (scratch checkout, a guest kernel
  from an older build via `TW_VM_KERNEL`, `VM_HUGEPAGES=1024`): the unit
  tests in `./hostd/vm` pass, but every VM test fails with "microVM not
  ready after 30s", including a cold `TestSmoke` that doesn't use uffd.
  A control run of `TestSmoke` built from origin/master fails the same way
  on that VM, so this is the environment (most likely the borrowed
  kernel), not this change. The snapshot-restore (uffd) path was **not**
  exercised against real Firecracker; it should be, with
  `make e2e-vm` on a working KVM host, to confirm real tables pass
  `validateRegions`.

## Gaps

- H16 (the other half of F8: the vmhelper accepts a loaded tenant VM as a
  snapshot base) is still open, #151.
- Other root-helper goroutines (`session.wait`, volume attach) don't have
  their own `recover`; a panic on the `serveConn` goroutine is covered.
