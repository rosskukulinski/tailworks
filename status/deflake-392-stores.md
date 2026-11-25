Last update: 2026-11-24 16:30 PT. Deflaked what #392 still had after #388: KV/TTL, the mgmt Queues test, and two more Queues failures that come from the test's 1 s FenceAfter.

# deflake-392-stores

Follows `status/deflake-387-queues.md` (#388). Tests only: no product code
changed.

## What was still flaky after #388

Every Queues sighting in #392 predates #388 and has #387's shape. I pulled
the failed CI runs' logs (`gh run view <id> --attempt N --log-failed`) for
PRs #370 and #372 and for master at e5e3e533, 7baa122d, f120640b and
a39b0249. Each Queues failure shows the waited-for message at `Attempts:2`
only, or out of retries with no record: a hostd-cancelled delivery counted
as an attempt. `DelaySeconds` (`d1` and `sl1` seen only as `Attempts:2`)
is the same failure, not a separate cause.

Four causes were left at master ed4d2831:

1. **`KV/TTL`** (`kv_test.go:172: before expiry: null, want "brief"`,
   PR #370 attempt 3). The test writes a key that expires at
   `time.Now().Unix() + 3`. Expiration is in whole seconds, so the read has
   2 to 3 s to land, after a journal append, the worker's conflicting write
   and its catch-up. On a loaded runner that chain took longer and the key
   had legitimately expired. This is a timing assumption in the test, not
   a KV bug.
2. **mgmt `TestQueuesDelayRetriesDLQ`** (seen on the mbp runner, noted in
   #388's status). It sends with `delaySeconds: 1` and asserts that a pull
   right after gets nothing. The retry-delay and lease-expiry steps assume
   the same. On a loaded machine the second passed first.
3. **`Queues/SlowHeartbeatReleasesBatch` (#388's own test) can livelock.**
   Reproduced under load: 1 of 6 `TestRealMgmtStores` runs failed with
   `no delivery of sh1 attempt 1 within 20s`, no `sh1` record, and three
   `handing it back` lines about 5.5 s apart. The fixture's `slow-once` was
   slow "on attempt 1". Since #388 a cancelled run is handed back uncounted,
   so the redelivery is attempt 1 too. It takes 4 s again, and the next
   stale moment of the lease cancels it again.
4. **A claim lost when its consumer loop stops.** Reproduced under load: 1
   of 8 `TestRealMgmtStores` runs failed in `ConsumerCrash` with
   `no delivery of h1 attempt 1 within 15s`. The log held `h1` as
   `Attempts:2` only, and hostd had no log line for attempt 1. mgmt had
   claimed `h1` (counting the attempt) for a pull whose consumer loop was
   cancelled at that moment, so nobody ran it. The lease expired after the
   test's 3 s visibility and `h1` came back as attempt 2. A cancelled pull
   is silent in hostd (`consumeQueue` returns on `ctx.Err()`), and that
   matches the missing log line.

Causes 3 and 4 both come from stale moments of the lease, and those are
frequent with the test's settings (`FenceAfter` 1 s, heartbeat every
100 ms). I added temporary logging (since removed) around `queueLoop`'s
stop and `tick`'s phases. Over 16 Queues runs under load, the consumer
loops stopped 21 times; 16 of those were the deliberate stops. The others
had `sinceHB` 1.08 to 1.49 s. Some came from a slow heartbeat round trip,
and one from `syncAssignments` taking 1.06 s while a facet's object node
was joining. `lastHB` moves only when a heartbeat returns, and the tick is
serial. #388 fixed the consequence for a run in progress. A claim in
flight, or a slow handler run, still pays.

## Fix

- `hostd/queues_test.go`: the Queues test's `FenceAfter` goes from 1 s to
  3 s. That is still short enough for `FencedHostConsumesNothing` to fence
  quickly, and well above the 1.5 s stalls seen under load.
  `SlowHeartbeatReleasesBatch` keeps its shape with the larger numbers: the
  heartbeat is delayed 4 s (stale 3 s in), and `slow-once` takes 8 s.
- `hostd/testdata/apps/queues` `slow-once`: slow on the message's first run
  in the isolate (a `Set` of ids), not on attempt 1. The redelivery is
  quick, so another stale moment can't starve it.
  `SlowHeartbeatReleasesBatch` still catches #387: with the old
  classification restored, it fails on both backends with `sh1` only as
  `Attempts:2`.
- `hostd/kv_test.go` `TTL`: a null "before expiry" read fails the test only
  if the read finished before the key's expiration, which would be a real
  bug. If it finished after, the test waits for the alarm to journal the
  delete, then rewrites the key with twice the window (3, 6, 12, 24 s; it
  fails at 24). An idle run is unchanged (3 s). Waiting for the journalled
  delete is needed: rewriting at once hit `store_conflict` against the
  alarm's delete. The journal-delete check is now a `dropped()` helper used
  in both places.
- `mgmt/queues_test.go` `TestQueuesDelayRetriesDLQ`: it runs on a
  `stepClock` set as the store clock (`m.now()` reads it for every queue
  decision), as `TestV1ContainerReservations` does. "Not due yet" is now
  deterministic. A claim already waiting still gets the delayed message
  once the clock moves on, which covers the once-a-second re-check. The
  crash case now also asserts that the message stays hidden while its
  lease holds.

## Open (for the maintainer)

- **Cause 4 is a product behaviour too.** When hostd stops a consumer loop
  (stale lease, self-fence, redeploy, shutdown) while a claim is landing,
  mgmt has counted an attempt for a batch nobody received. The message
  comes back only after the visibility timeout (60 s by default), with one
  retry used up, as if the consumer had crashed. With production settings
  (3 s heartbeat, 30 s `FenceAfter`) this is rare. Closing it fully
  conflicts with #140: a fenced host must not claim, so hostd can't simply
  let its pull finish and hand the batch back. A partial fix is for mgmt's
  `PullQueue` to release a claim, uncounted, when the caller's context is
  already done. That still leaves the window between writing the response
  and hostd reading it. I left it alone.
- **Stale moments:** a heartbeat only starts after the previous tick's
  `syncAssignments`, audit flush and release retries, so a slow sync makes
  the lease look stale (1.06 s seen in the test, while an object node
  joined). In production this needs about 27 s of stall to matter.

## Tests

"Load" below means the `mgmt` and `cmd/tw` `-race` test binaries running in
loops alongside a hostd `-race` binary at `GOMAXPROCS=4`, on an 18-core Mac
(a scratch script, not committed).

| Command | Result |
| --- | --- |
| Before: `TestRealMgmtStores` under load, `-count=6` | **1/6 failed**: `SlowHeartbeatReleasesBatch` (cause 3) |
| Before: `TestRealMgmtStores/Queues\|TestQueues$` under load, `-count=8`, with temporary logging | 16/16 ok; 5 unprompted loop stops (`sinceHB` 1.08 to 1.49 s) |
| With only the `slow-once` and KV/mgmt fixes: `^TestRealMgmtStores$\|^TestQueues$\|^TestWorkersKV$` under load, `-count=8` | **1 failed**: `TestRealMgmtStores/Queues/ConsumerCrash` (cause 4); the other 23 ok |
| Final: same three tests under load, `-count=10` | **30/30 ok**; 20 hand-backs, exactly the 2 deliberate ones per run, so no unprompted stale moment |
| `SlowHeartbeatReleasesBatch` with #388's fix reverted (cancel counted) | fails on both backends: `sh1` only as `Attempts:2` |
| `KV/TTL` with a temporary 3.5 s sleep before the read (simulates a slow runner), both backends | ok: logs `again with a wider window`, passes at 6 s |
| `go test -race -count=5 -run TestQueues ./mgmt` | ok |
| `GOMAXPROCS=2` mgmt binary `-run TestQueues -count=30`, alongside the hostd load | ok |
| `make lint` | ok |
| `make changes-check` | ok (no upgrade-relevant paths; no fragment, tests only) |
| Merged `origin/master` (dab6ceb2, clean), then `make lint` | ok |
| After the merge: `make test` on mbp (`sh ~/.claude/scratch/mbp-test.sh test`) | ok (exit 0; hostd 238 s, mgmt 137 s, cmd/tw 123 s) |
| After the merge: `make e2e-queues` | ok |

Not run: `make test-pg` (no store or migration change; the mgmt test uses
`SetClock`, as `TestV1ContainerReservations` already does under test-pg).
