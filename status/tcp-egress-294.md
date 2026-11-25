Last update: 2026-11-23 21:15 PT. Raw TCP egress (#294, ADR-052): stock `connect()` and `ctx.tailnet.connect()` built.

## What's built

- **Spike (pinned workerd 1.20261118.1).** A JS `globalOutbound` entrypoint
  gets a `connect(socket)` handler call (props intact, destination in
  `opened.localAddress`). `connect()` on a `style = proxy` external sends
  `CONNECT host:port` with no headers (`injectRequestHeaders` isn't applied)
  and sends nothing before the 200. Handler refusals reach the tenant only as
  "Network connection lost". `secureTransport: "on"` fails ("Incoming
  CONNECT with TLS not supported"), and so does `startTls()`. Workers RPC
  carries the socket streams. Recorded in ADR-052.
- **platform.js:** `connect()` handlers on `Egress` (facet: as the object)
  and `WorkloadEgress` (Worker and plain-DO: as the workload node).
  `tcpDial` opens the CONNECT tunnel, writes the preamble (loopback token,
  facet MAC over `CONNECT` + destination) and reads hostd's status line,
  keeping bytes the server sends first. `TailnetApi.tcpConnect` returns the
  streams for `ctx.tailnet.connect()` (plain-DO: `not_supported`).
- **hostd/connect.go:** hijacking CONNECT on the existing egress-obj and
  egress-wl endpoints (unix sockets, Landlock loopback listeners, microVM
  gateway). It checks the preamble, dials with `DialEgress`, and splices
  with half-close. `tls: "netd"` makes hostd originate TLS. Also a CLIENT
  span per flow, `egress` audit on close (bytes, duration), `deny` with the
  code, a 256-sockets-per-object cap (`socket_limit`), and sockets closed on
  instance stop. `localToken` passes CONNECT through (the token is in band);
  gen adds a `LOCAL_TOKEN` binding under Loopback.
- **runtime.ts:** `ctx.tailnet.connect(address, { tls?: "netd" })` returns a
  Socket at once, and `opened` rejects with a typed `TailnetError`.
  testing.ts: `createTestTailnet({ connect })`. New catalog codes:
  `not_supported`, `socket_limit`.
- **mgmt/connects.go:** `tailnet.connects` takes `"tag:db:5432"`. Preflight
  checks that port (`HasIPGrantPort`: `*`, `5432`, `tcp:5432`, `tcp:*`,
  ranges) and prints the snippet with it. Placement and tagOwners use the
  target.
- Docs: ADR-052, COMPAT (`connect` ⚠️, `hyperdrive`), README matrix,
  errors.md, limits.md, recipes.md (Postgres), egress-modes.md,
  tailworks-jsonc.md, the schema, and the RFC as-built row.

## Tests run

- `go test ./hostd/gen/ -run TestConnectEgress`: PASS (Worker, facet and
  plain, stock and `ctx.tailnet`; typed refusal; `tls: "netd"`).
- `go test ./hostd/ -run TestTCPEgress`: PASS. `TestTCPEgress`: whois
  identity per mode, refusals (loopback, metadata, sibling, not in netmap),
  audit, kill resets the socket. `TestTCPEgressInternet`: on, off,
  allowlist.
- `make e2e-pg-socket` (postgres:17-alpine on OrbStack):
  `TestTCPEgressPostgres` PASS, with postgres.js from a Worker and a facet
  object.
- microVM tier on this Mac (vz, assets from the main checkout's
  `dist/vz-assets-arm64`): `go test -tags e2evm -run TestE2EVMTCPEgress
  ./e2e/vm` PASS.
- `go test ./mgmt/ -run 'TestSplitConnect|TestHasIPGrantPort|TestPreflightConnectsPort'`: PASS.
- `node --test test/*.test.js` in js/packages/tailnet: 44 pass.
- `make test` (mini, before merging origin/master): PASS.
- After merging origin/master (#299, and #285, which took ADR-051, so this
  is ADR-052): `make lint` PASS. Also PASS: `go test ./mgmt/ ./cmd/...
  ./docs/ ./schema/ ./internal/compatdoc/... ./hostd/gen/` and
  `go test -run 'TestTCPEgress|TestLocalTokenConnect' ./hostd/`.
- `make templates-typecheck test-sdk`: PASS.

## Gaps

- Stock `secureTransport` TLS needs upstream workerd (a `connect()` handler
  that can serve TLS sockets).
- A plain-DO object's sockets belong to the workload node: killing one
  plain-DO object doesn't reset them.
- The Landlock loopback-token path (Linux only) is covered by unit logic, not
  by an end-to-end run here.
