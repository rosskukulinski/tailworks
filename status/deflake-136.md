Last update: 2026-11-21 PT. #136: KillLockObjectStopsIt fixed by #174; KillHolderReleases waits on the renewal instead of a sleep.

# deflake-136

## KillLockObjectStopsIt (the 503)

All three CI sightings came before #174 (merged 2026-11-18 06:38 UTC):
run 44070653715 (master, renew 503), 44238637362 and 44239891022 (renew
410, then the second node's acquire 503). None since, and none locally.

Mechanism: the 503 means the lock DO never got its killed marker. Before
#174, `killLocal` ignored workerd's answer to the admin kill, and every
later `killLocal` for the object (the heartbeat's `RevokeObjects` repeats
it for 10 minutes) returned at once because the object was already
`killed`. One failed or lost in-process kill left the DO unmarked for good.
Its requests then went to hostd's `ensure`, which no longer knows the
object, so the DO answered 503 (or 410 when mgmt refused the create). After
#174, a failed in-process kill is an error, and the next `RevokeObjects`
retries it (`awaitKill`).

Evidence:

- Injected a lost in-process kill (skipped the admin kill for `job:kill`)
  into the pre-#174 `killLocal`. `KillLockObjectStopsIt` failed 2 of 2 runs:
  `holder's renew after the kill: {Status:503 ... lock_unavailable "object
  Lock/job:kill does not exist and no request admitted its creation"}`.
- `go test -race -count=10 -run 'TestLockTemplate$' ./hostd` on master and
  on master with #174 reverted: KillLockObjectStopsIt 10/10 each. A healthy
  workerd doesn't lose the admin kill here, so the old code passes too.
- With this branch: `-count=15` on all of TestLockTemplate, then
  `-count=30 -run 'TestLockTemplate$/(KillLockObjectStopsIt|KillHolderReleases)'`:
  45/45 KillLockObjectStopsIt.

The CI bodies showed `Error:` empty, so the exact layer that answered
there isn't known. `lockReply` now keeps the raw body (and `Tw-Error`) of
any answer that isn't the lock template's own, so a future failure shows
which layer answered.

## KillHolderReleases (holder lost its lease before the kill)

Reproduced on master, 1 of 10 runs (`-race -count=10`, load average about 70):
`contender while the holder renews: {Status:200 Token:1000000002 ...}`.
The test slept 1.5 s and assumed the holder's 1 s lease, renewed every
333 ms, was still held. Under that load a renewal missed the deadline, and
the contender rightly got the lock. That's a test timing assumption, not a
lock bug (`ExpiresWithoutRenewal` covers expiry).

Fix: the holder takes a 3 s lease, renewed every 1 s. The test waits until
the status shows the deadline moved (a renewal landed, same token) instead
of sleeping, then checks the contender gets 409, then kills.
`docs/locks.md`'s measured figure is now "3.0 s after killing a holder of a
3 s lease" (measured 2.97 to 3.02 s over 15 runs).

## Also seen (not changed here)

- `TestLockTemplate/ClientWithLockRenewsAndFences`, 1 of 10 on master
  under the same load: `after withLock: {... Held:false Why:expired}`. That
  subtest uses the same 1 s lease. The lease ran out before the release.
  Filed as #196.

## Tests

- `go test -race -count=15 -run 'TestLockTemplate$' ./hostd`: ok, all subtests 15/15.
- `go test -race -count=30 -run 'TestLockTemplate$/(KillLockObjectStopsIt|KillHolderReleases)' ./hostd`: ok, 30/30 each.
- `make lint test` (macOS, after merging origin/master): ok.
