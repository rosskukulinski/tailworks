Last update: 2026-11-23 PT. #288: UnixUpstream returns workerd's early answer (410, 413, …) instead of 503 "write: broken pipe".

# fix-288-early-response

## Symptom

workerd answers a POST without reading its body (a killed object's 410
from `hostd/gen/js/platform.js` `__killed`, or a handler rejecting an
upload). With a body of about 16 KB or more, netd's `UnixUpstream`
returned `503 upstream_unavailable: upstream: write unix …: write: broken
pipe` (or `socket is not connected` on macOS) instead of the answer.

## Mechanism (Go 1.27.4, net/http/transport.go)

- A raw-socket probe against workerd: it reads the headers and about 8-16
  KB of body, writes `HTTP/1.1 410 Gone` with `Connection: close`, and
  closes. The next body write gets EPIPE, and the full 410 is still
  readable on the conn afterwards. So the response does reach the client
  side before the write fails (5/5 probes).
- `persistConn.roundTrip` waits in one `select` for `writeErrCh` (the
  write loop's result) and `resc` (the read loop's response). A non-nil
  write error returns at once: `pc.close("write error")` and
  `mapRoundTripError`, with no look at `resc`. Only the `pcClosed` branch
  checks for a response that raced the close. The read loop parses the
  410 concurrently. Whichever goroutine wins decides between the real
  answer and the 503. `GetBody` doesn't help: a fresh conn is never
  retried (`shouldRetryRequest`), and the error is not
  `nothingWrittenError`.
- PR #300's note holds: workerd announces `Connection: close` when it
  answers before the body arrives.

## Change

- `netd/upstream_early.go`: `KeepEarlyAnswer(tr)` wraps the Transport's
  dialer with a conn that, while a request is being written (httptrace
  `GotConn` to `WroteRequest`) and after at least one write of that
  request reached the conn, reports a write that failed because the peer
  closed (EPIPE, ENOTCONN, ECONNRESET, EPROTOTYPE) as a success, and drops
  that request's later writes. The Transport then waits for the response:
  workerd's answer, or a read error (still 503) if workerd left without
  one. Nothing is buffered, so bodies stay streamed (docs/limits.md).
  Dropping stops when the Transport closes the conn.
  Unchanged on purpose: a request's *first* write failing (a pooled conn
  workerd already closed, #287) still errors, so net/http still sees
  `nothingWrittenError` and retries when it can. Writes after the request
  (WebSocket after 101) report errors as before. A conn that dropped
  writes fails any later request's first write.
- `netd/ingress.go`: `UnixUpstream` uses `KeepEarlyAnswer(tr)` (one line).
  KeepBodyEOF (#298) and UpstreamIdleTimeout (#300) are unchanged.
- Not applied to the other reverse proxies (`hostd/node.go`,
  `hostd/containers.go`, `hostd/vm/agent.go`, `cmd/tw/dev.go`): they can
  hit the same race with their own upstreams, and can adopt
  `netd.KeepEarlyAnswer` if they do.

## Tests

- `netd/early_answer_test.go` `TestUnixUpstreamEarlyAnswer`: real workerd
  (`testdata/early-answer/`), a Worker forwarding to a DO that answers 410
  without reading the body, 200 sequential POSTs each of 200 B, 20 KB and
  200 KB, plus the same through `/read` (body read first).
  - Before (ingress.go reverted, `go test -race -count=3`): FAIL every
    run, 153-189/200 at 20 KB and 19-51/200 at 200 KB; without -race
    18/200 and 1/200.
  - After: `go test -race -count=5 -run 'TestUnixUpstreamEarlyAnswer|TestUnixUpstreamLateBodyRead' ./netd`: ok.
- `netd/upstream_early_test.go` (edge cases of the fix, raw unix upstream):
  `TestKeepEarlyAnswerWithoutConnectionClose` (unannounced close; the next
  GET still succeeds) and `TestKeepEarlyAnswerNoAnswer` (close mid-body
  with no answer gives a prompt 503). `-race -count=3`: ok. They pass
  without the fix too; they guard against the fix hiding failures.
- `make lint`: ok. `make test`: test-go failed in three packages while
  the machine ran at load average ~95 from other agents: `cmd/tw`
  TestCLIOAuth (client timeout), `mgmt` TestFeatureGatedPlacementAndCordon
  + TestUpgradeRun (20 m package timeout), and a `hostd` nil-pointer panic
  in the test helper `serveWhoisTCP` (connect_test.go:42, `who.Node` nil;
  raw TCP from #314, not HTTP, so not this change). Reruns at normal load:
  `go test -race -count=1 ./hostd ./netd/...` ok (hostd 319 s, netd
  87 s); `go test -race -count=1 ./mgmt` ok; `go test -race -count=1 -run
  TestCLIOAuth ./cmd/tw` ok; `make test-js` ok. The `serveWhoisTCP`
  panic looks like a flake worth its own issue (WhoIs returned no Node).
- `make changes-check`: no upgrade-relevant paths. Added
  `changes/fix-288-early-response.md` (kind: fix).
- No AGENTS.md extra suite covers netd ingress; none run.
