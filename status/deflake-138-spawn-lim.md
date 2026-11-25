Last update: 2026-11-23 21:30 PT. #138 root cause found and fixed: a net/http request-body race in our ReverseProxies.

# deflake-138-spawn-lim (#138)

## Root cause

`TestSpawnLimEnforcedAtReceiver` failed with `delegate: 500 Error: Network
connection lost` when the parent's `refund` MCP response was cut after its
headers. The cut came from the mcp node's `netd.UnixUpstream` ReverseProxy
(hostd ingress to workerd), and it is a race inside net/http:

1. For a Content-Length request body, `http.Transport`'s writeLoop reads the
   body once more after the declared length, to check for extra bytes
   (`transferWriter.writeBody`, `doBodyCopy(io.Discard, body)`).
2. workerd answers as soon as it has the whole body (the MCP SSE headers come
   before the tool runs). The proxy has `FlushInterval: -1`, so it flushes
   the headers at once. On that first flush `http.Server` closes the inbound
   request body (`chunkWriter.writeHeader`, the discard path).
3. If the writeLoop's extra read happens after that close (it was
   descheduled under CPU load), it gets `http: invalid Read on closed Body`.
   The writeLoop treats that as a request write error and closes the upstream
   connection, while the response body is still streaming.
4. The ReverseProxy's body copy then fails with `use of closed network
   connection` and aborts the downstream response. Alice's egress proxy logs
   `ReverseProxy read error during body copy: unexpected EOF`, and workerd
   reports `Network connection lost` at `await r.text()`.

The parent's `refund` is the only call in the test whose SSE body comes well
after the headers (about 10 ms, the `TwBudget` DO round trip). So it is the
only call where losing the race cuts a body that's still in flight. In the
other calls the close lands after the body has already been copied, which is
harmless. In one instrumented run the race fired 10 times in 120 iterations
and cut a body once.

Ruled out: request.signal abort in the agents SDK (workerd logged nothing on
the mcp side), workerd's 15 s idle keep-alive close versus UnixUpstream's
60 s (the conn was in use, and the close was local), and #288 (the body was
fully read).

## Fix

`netd.KeepBodyEOF` (netd/proxybody.go), called from `Rewrite`, wraps the
outbound body. Once it has returned `io.EOF`, later reads answer `io.EOF`
without touching the closed inbound body. It's applied to every production
`FlushInterval: -1` proxy: netd `UnixUpstream` and `EgressHandler`, hostd
`nodeIngress` (Node tier), container ports, and the microVM ingress. It's
also applied to the two `tw dev` proxies (cmd/tw/dev.go, dev_callers.go):
ReverseProxy flushes an SSE response at once even with the default
FlushInterval, so they can hit the same race.

## How it was reproduced

- macOS (18 cores): 80/80 passes, even with 8 parallel instances and 12 CPU
  burners. It never reproduced there.
- Linux (Lima twd-dev, 8 vCPU arm64): 6 parallel instances of the test
  binary, `GOMAXPROCS=2`, 8 busy-loop CPU burners, `TW_TEST_LOG=1`. Before
  the fix: 3 failures in 270 iterations (1/60, 1/90, 1/120), all the same
  signature as CI. A temporary close-stack trace on the upstream conn showed
  `persistConn.writeLoop -> close` right after `REQ body read: http: invalid
  Read on closed Body`.
- Deterministic test: `TestUnixUpstreamLateBodyRead` (netd/proxybody_test.go)
  delays the post-EOF body read until the headers are flushed. Without the
  fix, the `UnixUpstream` subtest failed 3/3 with `response body cut after
  the headers: unexpected EOF`. The `bare` subtest (a proxy without
  KeepBodyEOF) shows that the injected race still cuts the body.

## Tests run

- `go test -race -count=20 -run TestUnixUpstreamLateBodyRead -v ./netd`:
  20/20 pass.
- Linux stress with the fix (same load as above, 6 instances x 40):
  240/240 pass, 0 fail (before the fix: 3 fails in 270).
- `make lint`: ok.
- `make test` (after merging origin/master): pass. After adding the two `tw dev` call sites: `make lint` and `go test -race -count=1 ./cmd/tw/... ./netd/...`: pass.
- `make changes-check`: ok (no upgrade-relevant paths changed). No fragment
  needed.

Not run: the e2e suites (none of them cover this proxy path more than
`make test` does).
