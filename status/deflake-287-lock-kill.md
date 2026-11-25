Last update: 2026-11-23 PT. #287: UnixUpstream's idle keep-alive timeout is now below workerd's idle close (10 s, was 60 s).

# deflake-287-lock-kill

## Symptom

`hostd` `TestLockTemplate/KillLockObjectStopsIt` (CI run 45244249704): after
the kill, the holder's renew got its 410. Then c2's acquire got
`503 upstream_unavailable: upstream: write unix …/ingress.sock: write:
broken pipe`.

## Root cause

The error text shows which write failed. If any byte of the request had
reached the conn, net/http would have prefixed the error with
`net/http: HTTP/1.x transport connection broken:`. It has no prefix, so
it was a `nothingWrittenError`: the first write on a *reused* pooled
conn failed because workerd had already closed that conn. net/http
retries that case only when the body can be replayed (`GetBody`), and a
proxied body can't be.

workerd closes an idle ingress conn 15 s after its last response.
`netd.UnixUpstream` kept idle conns for 60 s. Between workerd's close and
net/http's read loop noticing the EOF, there is a window of about a
millisecond in which net/http still hands that conn to a new request.
The write then fails with EPIPE, or, if it got through, the response read
gets EOF.

Measured on `master` (an experiment harness: stock workerd with a
DO config, a front `httptest.Server` → `netd.UnixUpstream`, one upstream
per probe, 400 probes sending a 4-byte POST after 14.98–15.02 s idle,
`-race`): 3, 18 and 4 failures in three runs (25/1200). Every failure was
`503 … write: broken pipe` (the CI error) or `503 … upstream: EOF`. With
the fix: 0/400.

Why the lock test's request landed on an old conn: Transport's pool is
LIFO, so the acquire normally reuses the renew's conn, which was used a
moment earlier. But the killed lock object answers 410 without reading
the request body. net/http writes a proxied request's headers and body
in two writes (`FlushHeaders` for a body that isn't in memory). When
workerd reads the headers alone (a gap of ≥ 50 µs was enough in 73/100
raw-socket probes), it answers `Connection: close` and drops the conn.
The next request then takes an older idle conn, which can be one that
workerd is closing at its 15 s mark. A hostd hammer (1600 renew/acquire
calls on a killed lock, 8 in parallel, `-race`, CPU hogs) showed that
`Connection: close` drop 6 times. workerd announced every close it made
there; only the idle close is unannounced.

Relation to #138 / PR #298 (`netd.KeepBodyEOF`): that race makes
net/http close the upstream conn *itself*, after an extra body read
fails. A conn net/http closed itself is marked broken and never handed
out again, so it can't cause a nothing-written EPIPE on the next
request. At most it drops the top conn from the pool, like the
`Connection: close` drop above, and the idle-timeout change covers what
happens after that. The two fixes are independent. This branch doesn't
build on #298.

## Change

- `netd/upstream_idle.go`: `UpstreamIdleTimeout = 10 s` (doc comment
  explains the race and the numbers). `netd/ingress.go` uses it for
  `IdleConnTimeout`. net/http closes the conn itself before workerd
  does, and never hands out one that has been idle ≥ 10 s.
- `netd/upstream_idle_test.go`: `TestWorkerdKeepsIdleConnLongerThanUpstream`
  checks that workerd keeps an idle ingress conn open for
  `UpstreamIdleTimeout + 2 s` and still serves on it. It fails if a workerd
  upgrade shortens its idle close. Checked: with the timeout set to 14 s it
  fails ("closed … within 16s … EOF"). Runs in parallel, about 12 s.

Not changed: buffering small bodies so they get `GetBody`. That would only
cover the EPIPE variant, not EOF, and `docs/limits.md` says tailnet bodies
are streamed, never buffered.

## Tests

- `go test -race -count=30 -run 'TestLockTemplate$/KillLockObjectStopsIt' ./hostd`: ok (272 s).
- `go test -race -count=1 -run TestWorkerdKeepsIdleConnLongerThanUpstream ./netd`: ok.
- Before the change: `TestLockTemplate` 20× in a loop with 18 CPU hogs did
  not reproduce (20/20 pass). The race needs the 15 s coincidence above,
  which the idle-window experiment hits directly.
- After merging origin/master (e4d4422e): `make lint` ok. First `make test`:
  one failure in `hostd/gen` `TestGeneratedWorkflows/StepsRetrySleep`
  ("dial unix …/ingress.sock: connect: connection refused"). That package
  doesn't use `netd.UnixUpstream`. `-race -count=3` of that test passed, and
  a second full `make test` passed.
- `make e2e-locks`: ok (`TestLocksFailover` 77.6 s).
- `make changes-check`: ok (no upgrade-relevant paths, no fragment needed).

## Follow-ups (not done here)

- hostd's `inst.direct` client to workerd (`hostd/workload.go`) has no
  `IdleConnTimeout`, so it can hit the same race. Its requests mostly have
  no body or an in-memory one, which net/http retries in the
  nothing-written case but not after EOF.
- #288 (bodies ≥ ~16 KB answered without being read fail mid-write) is
  the same workerd behaviour as the `Connection: close` drop above, for
  bodies that don't fit in workerd's first read.
