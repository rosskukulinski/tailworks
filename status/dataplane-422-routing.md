# dataplane-422-routing

Issue #422: the data-plane chaos loop for routing (public ingress, service
bindings, WebSockets across moves). Ledger: `e2e/dataplane/ledger/routing.md`.
Process and invariants: `e2e/dataplane/LOOP.md` "Routing".

## What's here

- `e2e/dataplane/routing_test.go`: `TestRouting`, three stateful workloads
  (`testdata/routing/{a,b,ws}`), a public client on netd's public
  listener, a tailnet caller, WebSocket clients, a recorder; the routing
  nemesis (`move`, `paths`, `revoke-binding`, `hibernate`, takeovers, and
  TestDataplane's host faults); `dedupeNames` keeps testcontrol's MagicDNS
  names unique.
- `e2e/dataplane/check_routing_test.go`: N1–N6 and `TestRoutingReplay`.
- `e2e/dataplane/redeploy_test.go`: `TestRedeployAfterUndeploy`, the
  reproduction of #593 (skipped until it's fixed).
- `tw-hostd -test-public-listen-dir` (`internal/hostdrun/testfunnel.go`;
  `-local-dev` only): public nodes listen on loopback, so a hermetic test
  drives netd's public listener as Funnel would.
- `make e2e-dataplane-routing`, in nightly (30 min).
- Docs cite the tests: `docs/public-ingress.md` "Under faults",
  `docs/wrangler.md` service bindings ("When the target moves"), and the
  COMPAT rows for WebSockets, Hibernation, Service bindings and Public
  routes.

## Findings

- #572 (fixed in #579, merged into this branch): a fenced stateful
  workload kept answering open WebSockets for up to 3 s after its lease
  was revoked.
- #593 (open, needs a decision): redeploying after an undeploy onto a host
  that once held the workload never gets ready.

## Results

"Done when" for #422 is met: 3 clean 4-minute seeds per scenario on
macOS (public 1, 2, 3; bindings 1, 2, 3; websockets 1, 2, 3; routing 2, 3,
4), and at least one clean Linux run per scenario (lima twd-dev, arm64:
public 11, bindings 11, websockets 11 and 12, routing 11). The ledger has
every run, the reruns and why.

Gaps:

- #593 is open, so `revoke-binding` redeploys onto the host it undeployed
  from, and `TestRedeployAfterUndeploy` is skipped.
- A WebSocket left open across a hostd crash can stay black-holed until
  the client gives up (ledger HR1). N5 reports it as a note.
- No 10-minute soak.

## Tests run

- `make e2e-dataplane`-style runs: `TestRouting` (`go test -tags
  e2edataplane -run 'TestRouting$'`) for the seeds above, the ledger's
  run log has each. Post-merge smoke: routing 5, 2 min.
- `TestRoutingReplay` on routing 2 and websockets 2 (clean after the
  checker fixes).
- `TestRedeployAfterUndeploy` with `TW_DP_KNOWN_FAILURES=1`: fails (#593).
- `go test -run TestFenceClosesOpenWebSockets ./hostd/`, `go test
  ./hostd/`: ok (#579).
- `make lint`, `make lint-docs`, `make changes-check`: ok.
- `make test` (mbp runner): all ok except `cmd/tw:TestDevPython` (workerd
  not healthy in time) and `mgmt:TestWarmKeysRevokedOnCloseInParallel`
  (1.8 s over a 1.6 s budget); both pass when rerun here, and neither
  package is touched by this branch.
