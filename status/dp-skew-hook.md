Last update: 2026-11-24 22:45 PT. A test-build wall-clock hook for skewing a whole host (hostd and its workerds), for #415 H3.

# dp-skew-hook

Issue #415 (data-plane chaos loop) needs wall-clock skew on a host (ledger
H3, ADR-028). Until now only tw-mgmt had a clock hook
(`TW_TEST_WALL_CLOCK_OFFSET`, `-local-dev`). This lands the hook on its own
so #418 (Workflows), #419 (cron, alarms) and #421 (deploys) can build on it.

## What's built

- `internal/wallclock`: `Now()` is `time.Now()` in a production build.
  hostd's wall-clock decisions go through it: tickets and caller seals it
  mints for its workerds, the egress and connect MAC timestamps it
  verifies, and when a cron trigger is due. Durations (fence, timeouts)
  stay on the monotonic clock.
- `testhook_on.go` (`//go:build twtestclock`) is the only code that reads
  the hook's variables, so a production build has no way to shift its
  clock:
  - `TW_TEST_WALL_CLOCK_FILE`: the offset as a Go duration, re-read every
    100 ms, so a test steps it by rewriting the file.
  - `TW_TEST_CLOCK_PRELOAD`: a preload library. Every workerd hostd starts
    runs with it and follows the same offset (through `<file>.secs`), so the
    whole host reads one wrong clock. The sandbox's read list includes the
    library and the file.
- `internal/wallclock/testdata/skewclock.c`: the preload library (Linux).
  It shifts `CLOCK_REALTIME`/`_COARSE`/`TAI`, `gettimeofday` and `time`, and
  makes raw system calls. libfaketime was tried first: it hangs workerd
  before `main` (a futex wait), with and without its MT variant.
- `e2e/cliflow`: `BuildBinariesTags(t, "twtestclock")` and
  `BuildClockPreload(t)` (needs `cc`; returns "" off Linux).
- `make lint` vets the `twtestclock` build of `internal/wallclock` and
  `cmd/tw-hostd`.

## Tests

- `go test ./internal/wallclock/`: pass (offset steps, rounding, a bad file
  keeps the last offset, the preload environment).
- `make lint`: pass. `make test`: pass (macOS, after merging origin/master).
- Linux arm64 (Lima): the preload library built with `cc` and loaded into
  workerd (`workerd --version` runs; under libfaketime it hangs). In
  `e2e/dataplane` (branch `dataplane-415-finish`), the workload's
  `Date.now()` read +90.0 s after a +90 s step and the true clock after
  stepping back.

## Gaps

- macOS: no preload library (the hook still shifts hostd, but harnesses
  shouldn't skew a host whose workerds can't follow).
- The Node tier's `iat` (hostd/node.go) is not on `wallclock.Now`: the Node
  process isn't skewed, so they agree.
