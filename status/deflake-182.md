Last update: 2026-11-21 PT. TestRejoinRejectedRemints (#182) failed because of a real stuck-rejoin bug in netd, now fixed.

## Root cause

`watchNeedsLogin` (netd/registry.go) catches a saved node that control
rejects. It has two ways to do that: NeedsLogin, or an IPN bus `ErrMessage`
that lasts for `needsLoginGrace` (2 s). A node whose saved state is still
marked logged in, but whose key control has expired, produces neither. It
stays in **NoState** and retries its login over and over. Each attempt ends
with `weird: regen=true but server says NodeKeyExpired`. That is a plain
error, not a vizerror, so the backend never sends it as an `ErrMessage`. It
appears only as the `login-state` health warning. The rejoin then waits until
JoinTimeout (2 min) and holds a join slot the whole time.

How this produced the #182 failure: tailscale logs an ephemeral node out at
shutdown on a best-effort basis (5 s timeout). In the normal case the logout
lands, the rejoin starts in NeedsLogin, and the remint happens in about 2 s.
That was the result in all 60 runs here (`notify state=NeedsLogin` first,
every time). If the logout doesn't land, as can happen on a loaded CI runner,
"gone" rejoins as still logged in and takes the NoState path. The test's 90 s
context then ends with
`gone: netd: identity_pending: object gone node not ready: context deadline exceeded`,
which is exactly the CI error.

A probe test showed the same thing deterministically. A persistent node with
an expired key hung in 2 of 2 runs, with 30+ bus messages and no state change
or ErrMessage in any of them.

## Fix

- `netd/rejoin.go` adds `loginRejectedNotify`. It counts either an
  `ErrMessage`, or the `login-state` health warning when its error mentions
  `NodeKeyExpired`. This is the same warning tailscale's containerboot uses to
  detect a rejected auth key. Network errors reaching control also set
  login-state. They don't count, so a host that restarts while control is
  unreachable keeps its nodes' identities.
- `netd/registry.go`: `watchNeedsLogin` subscribes with
  `NotifyInitialHealthState` and uses that helper. The change is 4 lines.
- `netd/rejoin_test.go` adds `TestRejoinExpiredStillLoggedInRemints` with two
  subtests. One is a persistent node. The other is an ephemeral node whose
  logout was lost, simulated by restoring the state saved before Close. Both
  run with control expiring the key.

## Found along the way (not fixed here)

Issue #200. When the saved state includes tailscale's netmap cache
(`profile-data/.../netmap-cache`), `tsnet.Up` returns Running from the cached
netmap before control has answered. If control then rejects the node, netd
has already marked it Ready and nothing re-mints. The new test removes
`profile-data` so it always exercises the #182 path.

## Tests

- New test without the fix: both subtests fail after 20 s with
  `identity_pending ... context deadline exceeded`.
- With the fix, 4 parallel copies of
  `GOMAXPROCS=2 netd.test -test.race -test.count=12 -test.run 'TestRejoinRejectedRemints$|TestRejoinExpiredStillLoggedInRemints'`:
  96 of 96 passed. Each remint finished about 2.4 s after Rejoin.
- Before the fix, `TestRejoinRejectedRemints` alone, 6x10 with
  `GOMAXPROCS=2` and 4x10: 100 of 100 passed. The hang needs a lost
  ephemeral logout, which these runs never hit.
- `make lint test`: see the PR.
