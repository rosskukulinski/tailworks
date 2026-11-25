Last update: 2026-11-24 17:40 PT. A queue claim whose caller is gone is handed back uncounted (#412).

# fix-412-queue-claim-cancel

Found by the #392 deflake (`status/deflake-392-stores.md`, PR #407): under
load, `ConsumerCrash` saw `h1` only as `Attempts:2`. hostd's consumer loop
had been stopped (a stale lease) just as mgmt committed the claim from its
pull. Nobody received the batch, the lease expired after the visibility
timeout, and the redelivery counted a second attempt.

## Fix

- `mgmt/queueclaimgone.go` (new), with small hooks in `mgmt/queues.go`:
  - `PullQueue`: when a claim returns rows but the caller's context is
    already done, the rows are released at once (`SettleQueue` `Release`:
    visible now, attempt taken back) and the pull returns nothing.
    `database/sql` already rolls back a transaction whose context ends
    before `Commit`. This check covers the moment between the commit and
    the response.
  - `PullQueueMessages` writes its answer through `writePulled`. That
    encodes the answer, flushes it, and releases the leases if the write or
    the flush fails, because the caller never got the batch.
  - #140 holds: a fenced host's pull is cancelled, and anything it had just
    claimed goes straight back without being counted.
- `hostd/fakemgmt/queues.go`: the same two cases, so the fake and the real
  mgmt behave alike (#108). A cancelled caller's claim and an unwritable
  answer are undone in place, and a message whose lease has since moved on
  is left alone.

## hostd's side

Nothing more was needed beyond #388:

- A cancelled first pull is a plain error. `consumeQueue` returns, and mgmt
  now hands the claim back.
- A cancelled fill pull keeps the batch already held. `runBatch` then runs
  under a dead context, and `invokeQueue` classifies that as not run, so the
  batch is released uncounted.

A residual window remains over a network transport (unix socket or
tailnet). If the answer has been written to the socket and hostd cancels
before reading it, mgmt can't tell that from a delivery, so those leases
still expire. With `inproc` (single-process `tw up`, and hostd's
real-mgmt tests) there is no such window. The body is a pipe that isn't
cut off by the context. If the round trip is cancelled before the headers
arrive, the pipe is closed, the handler's write fails, and mgmt releases
the batch. hostd could narrow the network window with `httptrace` (cancel
only before the first response byte). I left that as a possible follow-up.

## Tests

| Command | Result |
| --- | --- |
| `go test -race -count=3 -run TestQueueStoreClaimForGoneCallerReleased ./mgmt` | ok |
| Same test with the release disabled | **fails**: `cancelled mid-claim: 0 message(s) visible again, want 2` |
| `make test-pg` (OrbStack Postgres 17) | ok; the new test passes on `sqlite` and `postgres` |
| `go test -race -count=1 -run Queue ./mgmt` | ok |
| `go test -race -count=1 -run 'TestQueues\|TestRealMgmtStores/Queues\|TestInvokeErr' ./hostd ./hostd/fakemgmt` | ok |
| `make e2e-queues` | ok |
| `make lint`, `make changes-check` | ok (fragment `changes/fix-412-queue-claim-cancel.md`, kind fix) |
| `make test` on mbp (`sh ~/.claude/scratch/mbp-test.sh test`), at origin/master 6a157ea4 | ok (exit 0; hostd 262 s, mgmt 141 s, cmd/tw 128 s) |
