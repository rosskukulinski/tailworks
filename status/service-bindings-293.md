Last update: 2026-11-23 PT. Issue #293: service bindings (`services`, WorkerEntrypoint RPC) and same-host workload-to-workload calls.

# Service bindings and the same-host hop (#293, ADR-053)

## What's built

- **netd same-host hop** (`netd/samehost.go`). A flow from an object to
  another object of the same registry is delivered in process to the
  destination's attributed ingress (a `net.Pipe` whose remote address is the
  source's tailnet IP), instead of being refused as a sibling hairpin.
  Checks: the destination is a peer in the source's netmap (unchanged
  resolution), and the destination node's packet filter accepts
  src → dst:port (`tsnet.Server.Sys().Engine.GetFilter().CheckTCP`, the same
  check WireGuard makes on a SYN). A Service VIP whose only reachable hosts
  are siblings goes to that sibling's Service ingress. Only the ingress port
  is served; public (Funnel) nodes are refused. `--allow-sibling-egress`
  now means "hairpin over WireGuard" (tests).
- **Service bindings.** `internal/svcbind` parses wrangler `services`
  (shared by `hostd/bundle` and mgmt). `env.<binding>` is a service binding
  to the platform's `ServiceBinding` (`hostd/gen/js/svc.js`); facets get it
  with their class/name. hostd (`hostd/servicebind.go`) resolves the
  binding via mgmt's new `GET /v1/tenants/{id}/workloads/{w}/service-bindings/{b}`
  (host role, lease-fenced; `mgmt/servicebind.go`, fakemgmt too), rewrites
  the egress request to the target's entry node, and netd sends it as the
  calling node. RPC is a POST with `Tailworks-Binding: v1; entrypoint=…;
  rpc=…` and a tagged-JSON structured-clone codec; the callee's hostd honours
  that header only from a tailworks node of the same tenant (node cert) on a
  workload entry node. The tenant main module exports a generated
  `__tw_binding` entrypoint (runtime `bindingDispatcher`) that runs named
  entrypoints and RPC methods wrapped like the default export; an RPC method
  reads its caller with `getCaller(this.ctx)`.
- `tw types` emits `Fetcher`; the `services` deploy warning is gone; host
  feature `service-bindings`; error codes `service_not_found`,
  `binding_forbidden`, `rpc_not_forwardable` (docs/errors.md, the runtime
  catalog, the mgmt spec enum).
- Fix on the way: hostd's egress `statusRecorder` had no `Unwrap`, so a
  WebSocket upgrade through egress failed ("non-Hijacker ResponseWriter");
  it now works.

## Tests run (all on mini unless noted)

- `go test ./netd/` — ok (new `TestChain/SameHostHop`: by IP, name, FQDN;
  other port refused; packet filter refusal and recovery;
  `TestService/EgressToSiblingServiceHop`).
- `go test ./hostd/ -run 'TestServiceBindings|TestLockTemplate'` — ok.
  `TestServiceBindings` runs against fakemgmt and the real mgmt: fetch,
  named entrypoint, streaming request and response, WebSocket, RPC
  (structured-clone round trip, `getCaller(this.ctx)`, thrown errors,
  `rpc_not_forwardable` for function/stream args and function/RpcTarget
  results, unknown method, missing target), `service_not_found`, the facet
  object as caller, a plain same-host tailnet fetch, a foreign tailnet node
  refused with `binding_forbidden`; then (fakemgmt) the target moved to a
  second host and the same set again. `TestLockTemplate` no longer needs
  `AllowSiblingEgress`.
- `go test ./internal/svcbind/ ./internal/features/ ./docs/` and
  `go test ./cmd/tw -run 'TestTypes|Wrangler'` — ok.
- `make lint` — ok.
- `make test-sdk` — ok (`TestCloudflareAgents/SameHostMCP` flipped from a
  refusal to a pass).
- `make templates-typecheck` — ok.
- `make e2e-locks` — ok.
- `make test` on the mbp runner (`mbp-test.sh test`, after the first merge
  of master): every package passed except `mgmt:TestTenantRolesConsistent`
  (the new host-only route needed its `tenantOperatorRoutes` entry). Fixed;
  `go test ./mgmt/` then passed, and `make test-js` passed (42 tests).
- After merging master again: `make lint`, `go test ./netd/ ./mgmt/`,
  `go test ./hostd/ -run 'TestServiceBindings|TestLockTemplate'` and
  `go test ./cmd/tw -run 'TestCLIReferenceUpToDate|TestTypes'` — ok.
- First CI run (PR #316): `e2e/local:TestNetdBinary` still asserted the
  old sibling refusal (now asserts the hop), and
  `hostd:TestLockTemplate/KillLockObjectStopsIt` hit the #287 flake, fixed
  on master by #300. After merging master again (ADR renumbered to 053;
  052 is tcp-egress): `make e2e-local`, `make lint`, `go test ./netd/
  ./mgmt/`, `go test ./hostd/ -run 'TestServiceBindings|TestLockTemplate'`
  — ok.
- Second CI run: `hostd:TestTCPEgress/Refusals` (from master's tcp-egress)
  expected a raw `connect()` to a same-host sibling's port 80 to be
  refused; the hop now delivers it, so the case dials a port nothing
  listens on (`dial_failed`). `e2e/real` SelfSSRF's sibling cases now
  accept the hop or `peer_not_visible` (not run: needs credentials).
  `go test ./hostd/...` and `make lint` — ok.
- `go test ./mgmt/ -run TestServiceBindingTarget` — ok (lease fence,
  undeclared binding, target never deployed, pending entry node, the
  answer, another tenant's same-named workload is not a target).

Not run: `make test-pg` (no store or migration change), the
credential/KVM/VZ suites.

## Findings

- **Node certificate race (existed before this change).** netd lets a new
  object's node send as soon as it has joined; hostd reports it ready to
  mgmt (which issues its certificate) a moment later. A callee that looks
  the caller up in between caches "not a tailworks object" for 60 s
  (`hostd/certs.go` `certCacheTTL`), so during that minute the caller's
  caps are not narrowed by its `lim`. Binding calls avoid it (hostd waits
  for the ready report before a named or RPC call); plain calls still see
  it. Worth its own issue: cache negative answers briefly, or hold egress
  until the ready report.
- Full-duplex HTTP (a response streaming while the request body is still
  arriving) doesn't work through netd ingress (Go's HTTP/1.1 server reads
  the request first). Each direction streams on its own.

## Gaps / next

- `tw dev` bindings to another workload; `environment`/`props`; RPC
  promise pipelining; Node-tier targets get `fetch()` only.
- Cross-host channel without grants (ADR-do-split option (a)) — revisit
  with that ADR.
