Last update: 2026-11-24 11:50 PT. Deflaked `hostd.TestRealMgmtStores/Queues` (#387): a queue delivery hostd cancels is handed back uncounted.

# deflake-387-queues

## Root cause

Every failed CI run has the same shape: the test waits for attempt 1 of a
message and the consumer's log holds that message as `Attempts:2` only, so
the first delivery left no record and the second counted as a retry.

Reproduced locally in the first try, with the hostd logs on
(`TW_TEST_LOG=1`, `GOMAXPROCS=4`, mgmt's and cmd/tw's test suites running
alongside as load):

```
WARN queue batch failed ... err="Post \"http://platform/_tw/queue\": context canceled" ms=0
WARN queue batch ran ... outcome=exception ms=26        (2.5 s later: attempt 2)
    queues_test.go:189: no delivery of a1 attempt 1 within 15s
```

The chain, in `hostd/queues.go`:

1. The test runs hostd with `FenceAfter: 1s, HeartbeatEvery: 100ms`. hostd's
   tick is sequential (heartbeat, then `syncAssignments`, audit flush,
   release retries), and `lastHB` moves only when a heartbeat *returns*.
   Under CI load (4 vCPUs shared by several `-race` test binaries since
   #367) one tick now and then takes over a second, so `cronLeaseFresh()`
   is false for a moment although no heartbeat failed and nothing fenced.
2. `queueWanted()` then lists no consumer loops ("a host that may have lost
   mgmt consumes nothing"), and `queueLoop` cancels the running one.
3. If the loop is in `claimBatch`'s 1 s fill window, the second `pull`
   fails with the cancellation, `claimBatch` swallows that (`break //
   deliver what we hold`) and hands the batch to `runBatch` under a dead
   context. `invokeQueue`'s POST fails at once (`ms=0`) with `context
   canceled`. The same happens when the cancel lands while queue() runs.
4. `invokeQueue` classified only a *dial* error as "not run". `context
   canceled` counted as a failed run: `settlement(batch, nil)` retried
   every message with its attempt counted. The message came back as
   attempt 2 and the test's wait for attempt 1 never ended. For `f1`
   (`max_retries` 1) the cancelled attempt also ate the only retry.

So the test's 15 s budget wasn't the problem, and the batch-timeout path
isn't slow: a delivery hostd itself cancelled was being charged to the
tenant's handler. That is a production behaviour too: a redeploy, a
self-fence or a hostd restart during a batch burned one of the consumer's
`max_retries` for a run that never finished.

## Fix

- `hostd/queues.go`: `invokeErr` classifies a failed POST. Cancellation by
  hostd (`ctx.Err()` set, not the queue() time limit) is now `errNotRun`,
  like a refused connection: `runBatch` releases the leases, the message is
  visible again at once and its attempt is not counted (ADR-039 already
  says this for a batch that never reached the handler). The queue() time
  limit and any other error still count as a failed run and retry the
  batch. This is the one tenant-visible change: `attempts` no longer grows
  for a delivery hostd aborted. Batch sizes and retry timing are unchanged.
- `hostd/queues_internal_test.go`: `TestInvokeErr` pins the four cases.
- Test hooks (`hostdtest.Hooks`, `fakemgmt.Server`, `realHooks`): one-shot
  `DelayHeartbeat(d)`, the host's next heartbeat succeeds after `d`. That
  is the CI trigger (a slow, successful heartbeat), distinct from
  `SetHeartbeatsDown` (failed heartbeats, which fence).
- `hostd/queues_test.go`: new subtest `SlowHeartbeatReleasesBatch` on both
  backends: a `slow-once` message (the fixture's queue() takes 4 s on
  attempt 1 and records only when done), then one heartbeat stalled 2 s
  while it runs. The lease goes stale 1 s in, the loop is cancelled, the
  batch is handed back, and the message must arrive as attempt 1 with no
  attempt-2 record. Without the fix it fails exactly like the issue:
  `no delivery of sh1 attempt 1 within 20s; log: [{K:sh1 Attempts:2 ...}]`.

The test's `FenceAfter` of 1 s is kept. It still makes a stale lease
likely under CI load, but the consequence is now a release and a
redelivery as attempt 1 about 1.5 s later, inside every wait's budget.

## Tests

Load = `go test -race -count=1 ./mgmt/ ./cmd/tw/` running at the same time
on an 18-core Mac; the Queues test pinned to few procs, as on CI.

| Command | Result |
| --- | --- |
| Before, `go test ./hostd -run 'TestRealMgmtStores/Queues' -race -count=3` (no load) | ok, 91 s |
| Before, same under load with `GOMAXPROCS=4 -v TW_TEST_LOG=1` | **1/3 failed**: `ExplicitAckSurvivesThrow`, `no delivery of a1 attempt 1 within 15s`, after `queue batch failed ... context canceled ms=0` |
| After, same under load, `-count=5` | 5/5 ok (no stale event fired this time; 5 × 15 batches ran) |
| After, under load with `GOMAXPROCS=1 -count=5` | 5/5 ok |
| After, `-run 'TestQueues/SlowHeartbeatReleasesBatch\|TestRealMgmtStores/Queues/SlowHeartbeatReleasesBatch' -race` | ok on both backends; log shows `queue batch not delivered; handing it back ... delivery cancelled`, then `queue batch ran ... ms=4004` |
| Same subtest with the old classification restored on one line | **fails**: `no delivery of sh1 attempt 1 within 20s; log: [{... K:sh1 Attempts:2 ...}]` |
| `go test -race -run TestInvokeErr ./hostd/` | ok |
| `make lint` | ok |
| `make changes-check` | ok (no upgrade-relevant paths changed; no fragment) |
| `make e2e-queues` | ok, 23 s |
| `make test` on mbp (`sh ~/.claude/scratch/mbp-test.sh test`, pre-merge tree) | 3 unrelated load failures, see below; the rest ok |
| Merged `origin/master` (clean), then `make lint` | ok |
| After the merge: `go test ./hostd -race -run 'TestQueues\|TestRealMgmtStores/Queues\|TestInvokeErr'` | ok, 82 s |
| After the merge: `make test` locally | ok (exit 0; hostd 181 s, mgmt 88 s, cmd/tw 82 s, netd 96 s) |

Not run: nothing else in the AGENTS.md table applies (no mgmt store,
placement, locks, observability or template change).

The three failures on the shared mbp runner, none in code this branch
touches, each passing locally with `-race -count=1`:

- `netd TestService/KillWithdraws`: `sending serve config: ... context
  deadline exceeded` (LocalAPI under load).
- `mgmt TestQueuesDelayRetriesDLQ`: "a delayed message was delivered
  early". The test sends with `delaySeconds: 1` and pulls at once; under
  load the pull landed after the second had passed, so the message was
  legitimately visible. A timing assumption in that test, worth its own
  fix.
- `hostd TestPublicIngress`: `connection refused` on the public port.

Seen while generating load, also unrelated: `cmd/tw
TestWranglerWarningsStoreKinds` fails under `-count=2` because
`hostdtest.RegisterProbeStore()` from the first iteration persists into the
second, where the "unregistered" assertion can't hold. `make test` uses
`-count=1`, so CI doesn't see it.
