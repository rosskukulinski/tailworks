Last update: 2026-11-21 PT. TestEgressWaitsForNewPeerPath deflaked (#139): a test race, not the host's 100.64.0.0/10 route.

## Root cause

Every recorded failure is `dial_failed: connect tcp 100.64.0.3:80: connection
was refused`. 100.64.0.3 is b1, the "with the path wait" call. The same error
appears on Linux CI (runs 44236438231 and 44236748733, 2026-11-18), where no
system Tailscale runs, so the macOS route overlap is not the cause.

The test dials the new peer the moment it is in a's netmap, before its object
is Ready. `WaitPeerReady` returns once the peer answers a disco ping, and the
peer can do that a moment before `Object.start` opens its ingress listener
(after `o.up` returns). A SYN that arrives in that gap is reset by the peer's
netstack. A temporary probe that recorded when b1 became Ready showed it
every time within 0 to 150 ms of the refused dial (7 failures in 4x12 runs).

Host routes don't affect the test: tsnet dials a tailnet IP through netstack
when `LocalBackend.PeerForIP` knows the peer (tsnet.go `UseNetstackForIP`),
and the test dials only after the peer is in a's netmap. On this Mac the
system Tailscale owns 100.64/10 (`utun11`) and the test passes.

## Fix

Test only (`netd/peerpath_test.go`). When the first dial is refused, the call
waits for the new object's `Ready` (its listener is open) and dials once more.
The time still counts from the first dial. The reset came back over WireGuard,
so the handshake under test had already worked. Waiting for Ready before the
first dial was tried and rejected: a then already has the path, "without the
path wait" drops from ~5 s to ~30 ms, and the test no longer tests anything.

## Tests

- Before, `go test -race -count=10 -run 'TestEgressWaitsForNewPeerPath$' ./netd`
  under machine load ~30: 1 of 10 failed (refused). With 4 copies of
  `-race -count=10` in parallel: 2 of 40 failed; `-count=12`: 7 of 48.
- After, 4 parallel copies of `-race -count=15`: 60 of 60 passed. 10 runs hit
  the refusal and recovered. "without" ~5.1-5.3 s, "with" 0.27-0.99 s.
- `make lint test`: see the PR.
