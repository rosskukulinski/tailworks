Last update: 2026-11-21 PT. #196: ClientWithLockRenewsAndFences waits on a renewal with a 3 s lease; withLock checks its deadline when fn returns.

# deflake-196-lock-renew

## The flake

`hostd` `TestLockTemplate/ClientWithLockRenewsAndFences` held a 1 s lease
renewed every 333 ms for 2.5 s. It then expected the status to say
`released`. On a loaded machine under `-race`, a renewal (or the release
right after `fn` returned) could land after the service's deadline. The
lock DO's alarm then freed the lease, and the status rightly said `expired`.
This is the same timing assumption that #136 removed from
`KillHolderReleases`.

Fix (mirrors #136): a 3 s lease (renewed every 1 s) held for 4 s. The
contender's `lock_held` check now waits until the status shows that a
renewal landed (`expiresAt` moved), instead of sleeping 1.5 s.

The `/with` request also no longer goes through `userGet` in a goroutine.
`userGet` calls `t.Fatalf`, so when the subtest had already failed, its
`t.Fatalf` panicked the whole `hostd` test binary ("Fail in goroutine after
... has completed"). The goroutine now hands back the code and body, and the
subtest checks them.

## The design question: should withLock report lease_expired on a late timer?

Yes. It was a real correctness gap, and it's fixed in
`templates/lock/src/client.ts`.

`withLock` only threw if `signal` was aborted, and only the expiry timer
(or a failed renewal) aborted it. When the event loop is too busy for that
timer to run, the deadline can pass while `fn` runs. `fn`'s promise then
resolves through microtasks, before any timer callback, so `withLock`
returned normally. The caller believed it held the lock throughout, but the
lease had lapsed, and another client may have taken it. The docs
(`docs/locks.md`) already promised that `withLock` throws once the deadline
passes without a renewal.

The fix: when `fn` returns, `withLock` also checks
`Date.now() >= cur.deadline` and throws `lease_expired`. The client's
deadline is `sentAt + ttlMs` (counted from when the granting request was
sent), so it is never later than the service's own expiry. A passing check
therefore means the service still held the lease for us when `fn` returned.
The check is conservative in one case: a renewal is in flight when the
deadline passes, and it would have succeeded. Then `withLock` reports
`lease_expired` though the service kept the lease. That's the safe
direction, because the client can't prove it held the lock.

A late renewal that succeeds needs no extra check. The service renews only
an unexpired lease with the same token, so the lease was held without a
gap.

Test: `js/packages/create-tailworks/test/lockclient.test.mjs` (`node
--test`, run by `make test-js` through the new `create-tailworks` `test`
script). It bundles `client.ts` with esbuild against a fake lock service.
`fn` busy-waits 600 ms on a 300 ms lease, so neither the renewal nor the
expiry timer can run. Without the fix it fails ("Missing expected
rejection"). With the fix it passes, along with the in-lease and renewal
cases.

## Stress (this Mac, 18 cores)

The natural flake didn't reproduce here even at a load average of 77-91.
The margin is shown by fault injection instead.

| Run | Before (master) | After (this branch) |
| --- | --- | --- |
| `go test -race -count=20 -run 'TestLockTemplate$' ./hostd`, 24 CPU burners | 20/20 pass | 20/20 pass |
| 3 × `hostd.test -test.count=10 -test.run 'TestLockTemplate$'` in parallel, 64 burners (load avg 77 before, 91 after) | 30/30 pass | 30/30 pass |
| Fault injection (temporary, not committed): the fixture delays its first renewal by 800 ms, `-count=3 -run 'TestLockTemplate$/ClientWithLockRenewsAndFences'` | FAIL: lease lapsed, contender got the lock (`{Status:200 ...}`), then a panic from `userGet`'s `t.Fatalf` in the goroutine | 3/3 pass |

## Tests run

- `node --test test/lockclient.test.mjs` (create-tailworks): 3/3 pass;
  without the client fix, 2/3 (the deadline test fails).
- `make lint`: ok (changes-check didn't require a fragment, but
  `changes/deflake-196-lock-renew.md` records the client's behavior change).
- `make e2e-locks`: ok (`TestLocksFailover` 79 s).
- `make test` (mbp runner, slot2): every Go package ok (`hostd` 296 s)
  except `cmd/tw` `TestCLIEndToEnd` (`tw deploy .../agent: exit status 1`).
  That's the known mbp-runner flake #210, unrelated to locks. Locally
  `go test -race -count=1 -run TestCLIEndToEnd ./cmd/tw` passes. make
  stopped before `test-js`, so `make test-js` ran locally: ok (includes
  the 3 new lock-client tests).
