Last update: 2026-11-23 PT. #309: hostd's clients to workerd and Node apps now drop idle conns before the server's idle close.

# fix-309-direct-idle

## Problem

Same race as #287 / PR #300: a client that keeps an idle keep-alive conn
longer than the server can be handed that conn by net/http in the moment
after the server closes it, before net/http's read loop sees the EOF. The
request then fails with `write: broken pipe`, `connection reset by peer`
or `EOF`. #300 fixed `netd.UnixUpstream`. hostd had more such clients.

## Audit

Every `http.Transport` in hostd/ and netd/ that keeps idle conns:

| Transport | Server | Server's idle close | Old | New |
| --- | --- | --- | --- | --- |
| `inst.direct`, process tier (`hostd/workload.go`) | workerd, unix ingress | 15 s | none (unbounded) | 10 s (`netd.UpstreamIdleTimeout`) |
| `inst.direct`, microVM tier (`hostd/workload.go`) | workerd in the guest, TCP :8080 | 15 s | none (unbounded) | 10 s |
| `interceptClient` (`hostd/workload.go`) | same as `inst.direct` | 15 s (5–6 s on Node) | none (unbounded) | copies `inst.direct`'s |
| microVM `ingressProxy` (`hostd/vm/agent.go`) | workerd in the guest, TCP :8080 | 15 s | 30 s | 10 s |
| Node tier `direct` + ingress (`hostd/node.go`) | the app's Node `http.Server` | 5 s (Node 18, 20), 6 s (Node 22.23, 25, 26) | 60 s | 3 s (`nodeUpstreamIdleTimeout`, `hostd/upstream_idle.go`) |
| `netd.UnixUpstream` | workerd | 15 s | already 10 s (#300) | unchanged |

Not changed, not workerd or app sockets: `netd/registry.go` egress (30 s,
arbitrary tailnet peers), `hostd/telemetry.go` (90 s, OTLP collector),
`hostd/r2/s3.go`, `hostd/mgmtapi`, the Firecracker API client
(`hostd/vm/helper`), and `hostd/containers.go` (keep-alives off). The
guest's `waitHealthy` client polls every 5 ms and stops at health, so it
never idles 15 s.

## Measurements

- workerd 1.20261118.1, TCP ingress (`-s ingress=127.0.0.1:PORT`, the
  microVM-tier path): closed an idle conn 15.004 s after the response.
  The unix-socket case (15 s) is #300's, still checked by
  `TestWorkerdKeepsIdleConnLongerThanUpstream`.
- Node `http.Server` defaults, raw TCP, time from response to the
  server's close: v18.16.0 5.003 s, v20.0.0 5.001 s, v25.1.0 6.003 s,
  v26.13.0 6.002 s; v22.23.5 (on mbp, the Node major CI uses) 6.004 s.
  All send `Keep-Alive: timeout=5`. Newer Nodes add
  `keepAliveTimeoutBuffer` (1000 ms) to the real close; 18 and 20 don't
  have it. The minimum, 5 s, is what the 3 s bound is set against.
- Race experiment against Node (not committed): 400 Transports, each GET
  then a non-replayable 4-byte POST after idling within ±20 ms of the
  Node close.
  - Node 25, idle ≈ 6 s: IdleConnTimeout 60 s failed 6, 4, 3 of 400
    (`EOF`, `connection reset by peer`); 3 s failed 0, 0, 0 of 400.
  - Node 20, idle ≈ 5 s: 60 s failed 14, 16 of 400; 3 s failed 0, 0.

## Change

- `hostd/workload.go`: `IdleConnTimeout: netd.UpstreamIdleTimeout` on both
  `inst.direct` transports (one line each); `interceptClient` copies
  `inst.direct`'s `IdleConnTimeout` with its `DialContext` (one added line).
- `hostd/vm/agent.go`: microVM ingress proxy 30 s → `netd.UpstreamIdleTimeout`.
- `hostd/node.go`: 60 s → `nodeUpstreamIdleTimeout`.
- `hostd/upstream_idle.go` (new): `nodeUpstreamIdleTimeout = 3 s`, with
  the reasoning and numbers.
- `hostd/upstream_idle_internal_test.go` (new):
  `TestNodeKeepsIdleConnLongerThanUpstream` starts a default Node
  `http.Server` on the `node` on PATH, measures its idle close, logs it,
  and fails if it is less than `nodeUpstreamIdleTimeout + 1 s`. Checked:
  with the constant set to 60 s it fails ("closed … after 6.001s").
  Skips when there is no `node`. About 5–6 s, parallel.

Known gap: an app that sets `server.keepAliveTimeout` below 3 s can still
hit the race. Honouring the `Keep-Alive: timeout=` response header would
cover that, but net/http has no hook for it.

## Tests

- `go test -count=1 -v -run TestNodeKeepsIdleConnLongerThanUpstream ./hostd`
  with Node 18.16, 20.0, 25.1 and 26.13 on PATH: ok (closes measured
  above). With the constant set to 60 s: FAIL, as expected.
- `make lint`: ok (after merging origin/master 7e339770).
- `make changes-check`: ok (no upgrade-relevant paths, no fragment).
- `make test` on mini: failed under load from other agents (load average
  ~97, 1,277 test processes): timeouts in `cmd/tw`, `mgmt` (untouched) and
  hostd's "workerd did not become healthy". Rerun on mbp instead
  (`mbp-test.sh test test-node`, Node 22) before and after merging
  origin/master: `make test` ok (all packages, `hostd` 394 s), `make
  test-node` ok (`TestNodeTier`, `TestNodeWorkflow`,
  `TestNodeTierSharedHostRefused`).
- Not run: `e2e-vm` / `e2e-vz` (the microVM ingress proxy and VM-tier
  `inst.direct` changes; need KVM or the Mac virtualization setup). The
  VM-tier bound rests on the workerd TCP measurement above.
