Last update: 2026-11-21 PT. Fix #140: a self-fenced host no longer claims queue messages.

# deflake-140

## Root cause

A real fencing gap, found with `TW_TEST_LOG=1`. The test still passed,
but in 7 of 8 runs of `go test -race -count=8 -run 'TestQueues$' ./hostd`
the log showed, 100 to 500 ms after `self-fencing stateful workloads`:

```
queue batch not delivered; handing it back ... messages=1 err="queue batch not run: workload queues has no running instance"
```

That one message is `op1`, which the test sends after the fence. Self-fencing
didn't stop the consumer loops. `queueWanted` drops them only at the next
reconcile (`queueReconcileEvery`, 500 ms), and a loop's claim is a long poll
(`queuePollWait`, 10 s) that was already waiting at mgmt. So the fenced
host claimed `op1`. Its workerd was already stopped, so it handed `op1`
back. With a workerd still up it could have consumed it while fenced.
Under heavy load the window is wider, which fits the one local failure in
the issue (no log was kept).

## Fix

- `hostd/queuefence.go` (new): every consumer loop runs under a
  `consumeGate` context. `tick` calls `stopConsumers()` when it self-fences,
  after setting `h.fenced`, which aborts every in-flight claim at once. No
  new gate is handed out until a heartbeat succeeds.
- `hostd/queues.go`: loops start under `consumeContext()`. A loop that
  ended by itself is dropped from the run map, so it restarts when wanted
  again. A loop also checks `cronLeaseFresh()` before each claim (a stale
  heartbeat that `tick` hasn't turned into a fence yet).
- `hostd/host.go`: one field (`consume`) and one call in `tick`.
- `hostd/fakemgmt`: `QueueClaims` counts messages claimed by hosts' pulls.
  `FencedHostConsumesNothing` now asserts that the fenced host claimed
  nothing.

## Tests

- New assertion on the old code: `go test -race -count=5 -run 'TestQueues$/FencedHostConsumesNothing' ./hostd`:
  FAIL 3/5, `the fenced host claimed 1 message(s)`.
- With the fix: same command, `-count=20`: ok 20/20.
- `go test -race -count=5 -run 'TestQueues$' ./hostd`: ok.
- `go test -race -count=2 -run 'Fence|Fenced|Cron|Queue' ./hostd`: ok.
- `make e2e-queues`: ok.
- After merging origin/master (with #152's cron change):
  `go test -race -count=3 -run 'TestQueues$|Fence|Cron' ./hostd`: ok;
  `make lint test` (macOS): ok.
