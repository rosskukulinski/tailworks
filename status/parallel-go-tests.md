Last update: 2026-11-24 10:00 PT. test-pg fixed: `scripts/test-pg.sh` runs with `-parallel 1`.

## test-pg (2026-11-24)

The PR's `test-pg` job (nightly.yml, on PRs that touch mgmt/) failed:
under `make test-pg` every mgmt test shares one Postgres database and resets
its public schema, so parallel tests dropped each other's tables (`relation
"tenants" does not exist`, `shared_owner_tag`). `scripts/test-pg.sh` now
passes `-parallel 1` to both of its `go test` runs, so that suite is serial
as before. The SQLite `make test` keeps the parallelism. `make test-pg`
locally (OrbStack Postgres 17): pass, 97 s. A per-test Postgres schema would
let this suite run in parallel too; not done here.

Earlier entry:

Last update: 2026-11-24 09:00 PT. First CI run (#367) passed: `test` job 10.0 min (recent median 11.2), CPU-bound on 4 vCPUs.

## CI result (2026-11-24, run 45440262910)

The `test` job passed in 10.0 min, against a median of 11.2 min over the
last two days. `make test` took 452 s (481–721 s before). Per package (s):
hostd 426 → 356, mgmt 395 → 345, cmd/tw 381 → 329.

The gain is small because the hosted runner has 4 vCPUs and these tests
are CPU-bound under `-race`: running them in parallel can't create cores
that aren't there. On the 18-core Mac the same tree runs `make test` in
about 3 min. So this change is what lets a larger runner (or more cores
anywhere) pay off, which it didn't before, when the tests ran serially.

Earlier entry:

Top-level tests in hostd, mgmt and cmd/tw run with t.Parallel().

## Why

CI's `test` job (median 10.8 min) is almost all `make test`, and `make
test` was bound by three packages that ran every test one after another:
hostd, mgmt and cmd/tw took 6–7 min each on CI. Before this change, 2 of
about 550 top-level tests in them called `t.Parallel()`. A larger runner
wouldn't fix that, because the extra cores would sit idle.

## What changed

- `t.Parallel()` in every top-level test that doesn't touch shared process
  state: 145 in hostd (plus the 2 that already did), 217 in mgmt and 99 in cmd/tw.
- A test stays serial if it, or a same-package function it calls, writes a
  package-level variable, calls `t.Setenv`, `os.Setenv` or `t.Chdir`, swaps
  `os.Stdout`/`Stderr`/`Stdin`, or calls `log.SetOutput`. Writes in `init`
  don't count. Serial tests finish (cleanups included) before any parallel
  test starts, so they still see no interference. That leaves 6 serial in
  hostd, 4 in mgmt and 79 in cmd/tw. The cmd/tw ones are mostly the
  in-process CLI tests (`jsonOut`, stdout capture), about 43 s in total.
- cmd/tw: `testmain_test.go` sets `TW_WORKERD` once for the test binary.
  Twenty tests each `t.Setenv`'d it to the same path, which is what kept the
  slow CLI and dev tests (`TestCLIEndToEnd`, `TestCLISingleProcess`, …)
  serial.
- mgmt: `imgN` (unique test images) is atomic. `keepHostsLive`
  (`hostlive_test.go`, taken from `publicHost`) heartbeats the hosts
  of `TestAPIPlacement`, which under load outlived the 10 s `HostLeaseTTL`
  (503 no_host "h3: not live", the issue #183 pattern).
- Back to serial after they failed under load:
  - `TestR2Streaming`: it samples the whole test process's heap.
  - `TestRedeployAfterLeaseExpired`: heartbeats are off, so its first
    deploy must land within `HostLeaseTTL`.

## Results (local, 18-core Mac, `-race`)

| | before | after |
| --- | --- | --- |
| hostd | 393 s | 154–182 s |
| mgmt | 270 s | 93–95 s |
| cmd/tw | 290 s | 85–110 s |

The hostd floor is now its serial tests: `TestR2Streaming` about 53 s, plus
the workerd restart tests.

Commands:

- `go test -race -count=1 -json ./hostd/ ./mgmt/ ./cmd/tw/` before and after:
  pass.
- `make lint`: pass.
- `make test`: four runs. The first two found the three tests above, and the
  last two passed (162 s and 195 s).
- `GOOS=linux go vet ./hostd/ ./mgmt/ ./cmd/tw/`: pass.

## Gaps / next

- If a test turns flaky under parallel load, the fix is to remove its
  `t.Parallel()` and add a comment saying why, as for the two tests above.
- More to gain: hostd's serial workerd restart tests, and cmd/tw's in-process
  CLI tests (they'd need `jsonOut` and stdout passed in, not global).
