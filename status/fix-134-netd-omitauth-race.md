Last update: 2026-11-17 PT. Fix the data race on `local.Client.OmitAuth` in `netd` `Object.up` (issue #134).

## Root cause

`Object.up` (`netd/registry.go`) called `srv.LocalClient()` twice. tsnet
returns the same `*local.Client` every time (`tsnet.Server.LocalClient`
returns `s.localClient`). The first result went to two goroutines
(`spans.watch` and, on a rejoin with saved state, `watchNeedsLogin`). After
`srv.Up` returned, the second call wrote `lc.OmitAuth = true` again on that
same client. A watcher whose `WatchIPNBus` request was scheduled late read
`OmitAuth` in `DoLocalRequest` at the same time, which the race detector
reported in `TestRejoinFromSavedState` on CI.

## Fix

`up` now gets the client once, sets `OmitAuth` once before it starts either
watcher, and returns that same client. No other code writes the field
after goroutines are using the client:

- `spans.watch` gets the client from `up`, so it is covered by the same fix.
- `netd/nettest` and the tests (`service_test.go`, `peerpath_test.go`,
  `internal/tssvc`, `e2e/placement`) set `OmitAuth` once, right after
  `Up` returns and before anything else uses the client.
- `cmd/tw` (`link.go`, `login.go`) and `hostd/paths.go` only read the client.

## Regression test

None added. The race needs a watcher goroutine to send its first local API
request after `Up` has returned. That depends on the scheduler, and hitting
it on purpose would need a test hook inside `up` (a hotspot file). Unfixed
master did not reproduce locally in 150 runs (`-count=30`, then
`-cpu 1,2,4 -count=40`). The race only shows on a loaded CI runner. The
evidence is that the second write is gone, and the `-race` count loop below
passes.

## Tests

- `go test -race -count=30 -run TestRejoinFromSavedState ./netd`: ok (45 s).
- `go test -race -count=3 ./netd`: no data races. One failure,
  `TestEgressWaitsForNewPeerPath`, is the known macOS-local failure #139
  (overlap with the machine's own Tailscale 100.64.0.0/10). Unrelated.
- `make lint test`: ok. (An earlier run under heavy parallel load failed only in `netd`, the #139 test above.)
- `make changes-check`: ok, no upgrade-relevant paths changed, so no fragment.
