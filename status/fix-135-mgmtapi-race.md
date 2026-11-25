Last update: 2026-11-17 PT. #135 fixed: hostd.New no longer writes the caller's mgmtapi.Client.

# fix-135-mgmtapi-race

## Root cause

`hostd.New` ran `cfg.Mgmt.HostID = cfg.HostID`, which wrote to the
`*mgmtapi.Client` the caller passed in. `hostdtest.Env.Restart` reuses
`e.cfg`, and with it the same client, for the new Host. A `retryNode` backoff
timer (`time.AfterFunc`, `hostd/objects.go`) left over from the previous Host
could still be calling `h.mgmt.Assignments` on that client, and `setHeaders`
read `HostID` without synchronisation. I reproduced it at e62f70a with
`hostd.test -test.run TestWorkersKVMove -test.count=30 -test.cpu 1,2,8`, and
it tripped within the first few iterations. The full report is on #135.

## Fix (immutable after start)

- `mgmtapi.(*Client).WithHostID(id)` returns a new Client with the same Base,
  HTTP transport and token source and the given identity. The doc comment now
  says a Client's exported fields are configuration, set before the first
  request and never written after.
- `hostd.New` makes a copy with `WithHostID` only when the client's HostID
  differs from `cfg.HostID`. It reads the caller's client and never writes
  it. This happens right after the required-field check, so the default
  `mgmtQuota` also gets the client with the identity.
- `internal/hostdrun` sets `mc.HostID` when it builds the client, before the
  first request. Its other holders (`rt.Client`, the image puller's
  `mc.OCI()`) used to get the header only because `hostd.New` mutated the
  client. They still get it.
- The other client fields (`HTTP`, `TokenFile`, `Token`) are only set where
  the client is built (`cmd/tw`, `hostdrun`). The token cache is already
  guarded by `tokMu`.

## Tests

- `hostd/mgmtapi/client_test.go` `TestWithHostIDLeavesClientAlone`: runs
  requests concurrently while `WithHostID` is called, then checks that the
  original is unchanged and that each client sent the right headers
  (`go test -race -count=3 ./hostd/mgmtapi/`: ok).
- `TestWorkersKVMove`, `-race -count=30 -cpu 1,2,8` (race-built test binary):
  before the fix it raced in the first few iterations. After the fix, 90/90
  runs passed with no race reports, on a machine loaded with `make test` and
  other worktrees' suites.
- `make lint`: ok. `make test`: ok. `make e2e-placement`: ok
  (TestD1SurvivesRestartAndMove, TestSmartPlacementHermetic,
  TestR2MoveBetweenHosts). `make changes-check`: ok, no fragment needed.

## Left

- The previous Host's `retryNode` `time.AfterFunc` timers aren't stopped by
  `Close`, so they can fire once after it, with `h.ctx` already cancelled.
  I left that out of scope: they no longer share a client that anyone writes.
