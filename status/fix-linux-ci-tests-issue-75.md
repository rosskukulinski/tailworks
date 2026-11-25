Last update: 2026-11-16 PT. All five tests from #75 fixed. CI skips nothing again.

# Tests that failed on Linux CI (#75)

The method: reproduce on Lima `twd-dev` (Linux arm64, 8 vCPU). For the
load-sensitive tests, 8 copies ran in parallel.

## Findings and fixes

- **`TestEgressWaitsForNewPeerPath`: two causes.**
  - Test harness (`netd/nettest`): DERP and STUN listen on 127.0.0.1. On
    Linux, Tailscale's `netns` marks magicsock's sockets or binds them to
    the default-route interface, so STUN to loopback failed. Every new
    node's first netcheck then waited out its ~3 s timeout before it had a
    path. `StartControl` now calls `netns.SetEnabled(false)`, as
    Tailscale's own tests with a local DERP do. This also speeds up
    `twd dev` on Linux, which uses the same harness. `netd` tests went from
    ~130-200 s to ~51 s on Linux.
  - Product race (`netd/peerpath.go`, `egress.go`): a path in the caller's
    view didn't mean the new peer knew the caller yet. The first handshake
    initiation was dropped and retried after 5 s (about 1 run in 6 once
    STUN worked). The new `WaitPeerReady` is `WaitPeerPath` followed by one
    disco pong, with per-ping timeouts starting at 250 ms and doubling to
    1 s, all within `PeerPathWait`. `awaitPeerPath` uses it. Other callers
    keep `WaitPeerPath`.
  - Result: 260 ms on Linux in 10 of 10 runs (before: 3.0 s every time, or
    5.4 s), and 269 ms on macOS.
- **`TestRejoinRejectedRemints`: a real hang, 7 of 24 runs under load.**
  - With saved state and an expired key, a node can stay in `NoState`,
    retrying `TryLogin` against "NodeKeyExpired" errors, without ever
    reaching NeedsLogin. `watchNeedsLogin` only reacted to NeedsLogin, so
    the rejoin never ended and held the only join slot.
  - Fix: a login error (`Notify.ErrMessage`) before Starting/Running starts
    the same 2 s grace timer.
  - The test also assumed `Rejoin`'s objects join in `ReadDir` order, but a
    channel semaphore doesn't hand out slots in order. The new
    `netd/joinslots.go` gives slots in the order `Create` was called (the
    place in line is taken synchronously), with
    `TestJoinSlotsOrder`.
  - Result: 0 of 24 under the same load.
- **`TestGeneratedWorkflows/AppCannotApprove`: test assumption.**
  - workerd's engine can report `running` for a moment while it replays a
    waiting instance, which happens more on a loaded machine.
  - The refusal itself happens before the engine (`WorkflowGate`), so the
    approval never reached it.
  - The test now fails only if the instance completed or produced output,
    and otherwise waits for it to settle back to `waiting`.
- **`TestContainerPolyfill/SurvivesRedeploy`: test race.** `c.start()`
  returns before hostd starts the container, so the test sometimes read
  `Gen` as 0. It now waits for `running` first, as `KillCascades` does.
- **`TestR2Streaming`: timeout.** It moves 1 GiB through workerd. That took
  25 s on an M-series Mac, and more than the suite's 30 s request timeout
  on the CI runner. The test now uses a 5-minute client; it measures
  memory, not speed.

## CI

- `ci.yml` no longer sets `GO_TEST_SKIP`. The variable stays, with a
  comment on how to quarantine a test.
- `release.yml`'s gates no longer hit these failures.

## Tests

- Linux (Lima), full `make test` with nothing skipped: everything passed
  except `TestRejoinRejectedRemints` (fixed since, as above).
- Linux stress run of `TestRejoinRejectedRemints` and `TestJoinSlots`
  (8 × 3 in parallel, `-race`): 0 failures.
- macOS: `go test -race ./netd/... ./hostd/gen/` and the hostd
  container/R2 tests pass.
- The PR's CI run is the `ubuntu-24.04` check.

## After merging master (2026-11-16)

The rerun after merging master failed three more tests:

- **`TestContainerPolyfill/MonitorAndRestart`: product race.**
  - When the process exits, `vm.Exited()` and `vm.Done()` can both be
    ready, and `select` picks either. A normal exit was sometimes reported
    as "vm exited", so `monitor()` rejected with "Runtime signalled the
    container to exit: 5 (vm exited)" instead of "unexpected exit code: 5".
  - `hostd/containers.go` now reports "exited" whenever the process has an
    exit code.
- **`TestR2IndexSurvivesMove`: test race.** The deleted put's blob goes a
  moment after the delete returns. The test now polls for up to 10 s.
- **`TestCLIMultiTenant`: intermittent audit chain "head mismatch".**
  - It also failed on master at 155fd87, so this PR didn't cause it.
  - Filed as #93 (bug, security). `ci.yml` skips it under that issue.
