Last update: 2026-11-15 13:50 PT. Issue #43: workerd crash recovery (root cause, group reap, never-give-up backoff, `crashloop` status).

# workerd crash recovery (issue #43)

## Root cause (with evidence)

The live install that failed (`~/.twd`, `VERSION` 0.1.0-1b4599a1, binary
built 2026-11-14 21:14) predates commit 1079ff8 (2026-11-15 08:58, the
queues WIP). That commit added the `os.Remove(ingress.sock)` before a
restart. At 1b4599a1 `onExit` respawns without unlinking. On macOS
(Seatbelt), workerd binds `ingress.sock` itself, and a crash leaves the
file behind, so every restart failed with `bind(): Address already in use`.
After 5 restarts hostd gave up.

- The socket in the failed instance dir still has the original mtime
  (`2026-11-14T21:35:54`, the deploy time), not a re-bind time, and no
  process holds it (`lsof` is empty). The old workerd was gone; the file
  was never removed.
- The same build of this branch with only the unlink disabled reproduces
  the production log line for line (`attempt=2 … bind(): Address already in
  use (os error 48)`, one new line per attempt). With the unlink, a
  `kill -9` recovers in about 270 ms.
- Not the cause: a surviving sandbox parent. `sandbox-exec` and the
  Landlock launcher exec workerd in place. The pid hostd waits on is
  workerd's own (`ps -o comm`, asserted in the test), and its process group
  is just workerd.

Found while reproducing: the log is a pipe (`obs.RotatingFile`, not an
`*os.File`). If anything in the group outlives the process hostd started
and holds the pipe, `cmd.Wait` blocks forever. hostd then never sees the
exit, never restarts, and `stop()` hangs. A wrapper-script test shows it:
without `WaitDelay` the test times out. The Node tier has the same pipe, so
it gets the same fix.

## What changed

- `hostd/restart.go` (new): the restart policy, `reapGroup`, the
  crashloop/recovered transitions and `watchRecovery`.
  - After any exit, hostd SIGKILLs the process group and waits until it is
    empty (at most 5 s). Then it unlinks `ingress.sock` (a failed unlink is
    logged) and respawns.
  - `cmd.WaitDelay = 1s` for workerd and node.
  - Once the instance has been healthy: backoff of 200 ms doubling to
    5 min, and hostd never gives up. The count resets after 30 s up. From
    the 2nd exit in a row the status is `crashloop` with
    `workerd exited N times in a row; restarting in D: <log tail>`. It
    goes back to `active` once a restart answers health. A spawn error
    counts as another exit.
  - Before it was ever healthy (a deploy): 3 restarts, then
    `waitHealthy` returns at once and the deploy is `failed` (about 1.6 s
    in the test, not the 20 s health timeout).
  - `crashloop` counts as live: the workload keeps its lease, nodes and
    pool. Unassignment still undeploys it. Cron and queue consumers pause,
    and the Service is withdrawn until it recovers.
  - The microVM tier goes through the same `onExit`.
- API: `DeploymentStatus` enum gets `crashloop`, one value per line with
  descriptions. `make api` regenerated the generated files. mgmt stores the
  status as-is. Release-note fragment: `protocol`.
- `WorkloadStatus.Restarts` (hostd status).
- CLI: `twd ls` already prints `error:` (lserror.go). `twd deploy` waiting
  sees `crashloop` → `deploy_crashloop` (docs/errors.md).
- Console: `badge("crashloop")` is red.
- Docs: docs/observability.md "Crash loops", docs/errors.md.

## Tests

`go test ./hostd -run TestWorkerd -v` (macOS, Seatbelt; real workerd):

- `TestWorkerdCrashRecovery/KillWorkerd`: kill -9 → new pid, serves, old
  group gone.
- `TestWorkerdCrashRecovery/CrashLoop`: 3 kills in a row → `crashloop`
  (restarts ≥ 2), then `active` again and serving.
- `TestWorkerdWrapperKilled`: kill -9 of a wrapper parent → its workerd
  child is killed, the group is gone, and a new one serves. Fails without
  `WaitDelay`.
- `TestWorkerdDeployTimeFailure`: workerd exits at once → `failed` in about
  1.6 s, never `crashloop`, and it stays failed.

`go test ./hostd -run 'TestRestartBackoff|TestLiveStatus|TestReapGroup'`
covers the schedule, the live statuses, and reaping a group whose leader
died.

## Open / not done

- Linux/Landlock was checked by reading the code only (no Linux host
  here). Its ingress listener is hostd's inherited fd, so there is no
  unlink. The group reap is what keeps a survivor from accepting on the
  shared listener.
- The Node tier already retried forever (250 ms → 10 s). It does not
  report `crashloop` yet.
- Deployments already `failed` on old installs need one `twd deploy`. No
  auto-retry of `failed` was added on purpose.
