Last update: 2026-11-22 PT. Deflaked `hostd TestContainerTimers` (#239) and `cmd/tw TestCheckDevPort`. Both are test-only fixes.

## hostd TestContainerTimers (#239)

CI failure: `map[idle:stopped inactivity timeout old:running ]`.

Root cause: the test waited for `procs.Live() == 0` and then read
`h.Containers()`. `ctrManager.finish` kills the VM (`vm.Stop`), and the fake
backend drops the process from `Live()` as soon as it exits. Only after that
does `finish` take `c.mu` and record `stopped` / `max lifetime`. Under load the
test can read the state in between. The product behaves correctly: a run is
`running` (with `stopping` set) until `finish` records it as stopped, and
`monitor` waits on `c.done`, which closes after the state is set.

Fix: the test now waits on each container's `c.done` channel (closed after the
state and reason are recorded). Then it checks that `procs.Live()` is 0, and
then it checks the reasons.

Reproduction: the natural window is too small to hit locally. 0 failures in
420 runs under CPU load (`-race`, 4 parallel `-count` runs, 24 `yes` hogs,
also with `GOMAXPROCS=2`). To confirm the cause, I added a temporary 150 ms
sleep in `finish` between `vm.Stop` and the state update. It was not
committed. With that sleep:

| | runs | fails |
| --- | --- | --- |
| old test | 40 | 40 (exact CI message) |
| new test | 40 | 0 |
| new test, no injection, `-race` under load | 120 | 0 |

## cmd/tw TestCheckDevPort

CI failure (run 44993457008): `::1 only: … port 45265 is in use on 127.0.0.1`.

Root cause: the test took a port from `[::1]:0` and assumed the same number
was free on 127.0.0.1. Ports are allocated per address, so a parallel test
holding a 127.0.0.1 listener on that number fails the check. The "free port"
step had the same kind of race: it closed a 127.0.0.1 port and then checked
it. `checkDevPort` itself is correct, because 127.0.0.1 really was taken.

Fix: a `checkFreeOn4` helper retries with a fresh kernel-assigned port, up to
50 times, while `checkDevPort` reports that 127.0.0.1 is taken. Any other
error, or a missing `[::1]` warning, still fails the test.

Reproduction: a helper held a 127.0.0.1 listener on every other port of the
ephemeral range (8180 ports) while the test ran with `-race -count`:

| | runs | fails |
| --- | --- | --- |
| before | 50 | 50 |
| after | 200 | 0 |

## Tests run

- `make lint`: ok
- `make changes-check`: ok (no upgrade-relevant paths changed)
- `make test` (mbp runner, `-race`): ok, 110 packages, exit 0
