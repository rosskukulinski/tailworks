Last update: 2026-11-21 PT. H22 (#155): `svc:` grant caps verified on a real tailnet; Service flows now see the Service's caps, not the node's.

# sec-155-svc-caps

## Finding (live, test tailnet 2, production control)

`tssvc.TestRealServiceCaps` (`make e2e-svc-caps`): caller `tag:ttest`, host
`tag:ttest-rt-svch`, one cap granted to `svc:ttest-rt-caps` and another to
`tag:ttest-rt-svch`.

- Plain `WhoIs` (what netd ingress and mgmt used) computes caps for the
  node's own address on every flow. Through the VIP it returned only the
  tag grant's cap; the `svc:` grant's cap was missing. With the tag grant
  removed (no tag-to-tag grant at all), plain `WhoIs` on the VIP flow had
  no caps.
- `WhoIsForIP(conn local addr)` and `WhoIsForService` return the `svc:` cap
  only on VIP flows (IPv6 VIP from MagicDNS and the IPv4 VIP), and the tag
  grant's cap only on node flows.

So caps on Service flows were both narrower (svc caps missing) and wider
(node-tag caps present) than the policy reads. Medium, as in the ledger.

## What changed

- `internal/tssvc/whois.go`: `tssvc.WhoIs(ctx, lc, remoteAddr)` uses
  `WhoIsForIP` with the local address from `http.LocalAddrContextKey`;
  falls back to plain `WhoIs` without one.
- `netd/ingress.go` (`serveIngress`) and `internal/mgmtd/mgmtd.go` (mgmt's
  WhoIs) call it. One line each.
- `docs/services.md`: verified behaviour (caps follow the dialed address;
  put a Service caller's caps in the `svc:` grant), a Measured row, a note
  on the `svc:tw-mgmt` admin grant.
- `security/LEDGER.md`: `LIVE_TARGETS` names this run; H22 **fixed**.
  `security/RED-TEAM.md`: a note with the measurement table.
- `Makefile`: `e2e-svc-caps` (tailnet 2); `e2e-svc` now runs
  `-run 'TestRealService$'` so it doesn't pick up the new test.

## Compatibility (coordinator review)

- **Generated policy never put caps on workload tag grants.** The deploy
  preflight checks tagOwners, `tailnet.connects` (ip only),
  `autoApprovers.services` and public nodeAttrs. `tw policy`'s caller grant
  is network-only. No generator emits app caps for workloads, so caps on a
  tag grant are always the operator's own. Callers of a Service already
  needed a `svc:` grant for network access (a tag grant doesn't cover the
  VIP), so only caps that sit on a tag grant alone are lost.
- **New preflight warning** (`mgmt/svccaps_lint.go`, one appended line in
  `Preflight`): for a Service workload, each (src, cap) granted on the
  workload tags but not on the `svc:` name becomes an `AccessWarnings`
  entry with the grant to add. `tw deploy` and `tw policy` print it. It
  is a warning, not a failure: the caps are user policy, and calls to the
  node name still get them. Test: `mgmt.TestServiceCapWarnings`.
- **`tw policy` / `tw deploy` caller grant:** a Service workload also gets
  `{ "src": ["autogroup:member"], "dst": ["svc:<name>"], "ip": ["tcp:80"] }`
  (`serviceCallerGrant` in `--json`, schema and `docs/cli-json.md`
  updated). Test: `TestServiceCallerGrant`.
- **HA `svc:tw-mgmt`:** `docs/services.md` already puts the admin cap on the
  `svc:tw-mgmt` grant, and so does the H12 e2e policy
  (`e2e/multimgmt/svc_real_test.go`). Single-mgmt `tw up` (`AdminGrant` to
  `tag:tw-mgmt`, reached by node name) is unchanged.
- `changes/sec-155-svc-caps.md`: kind security, `upgrade: [config-default,
  cli-json]`, with the policy lines operators must add.

## Tests

- `netd.TestServiceCapsScopedToVIP` (hermetic, testcontrol with a raw
  packet filter: one cap to the VIP, one to the node's addresses). Failed
  before the fix (Service flow: svc cap missing, node cap present), passes
  after.
- `tssvc.TestDialedAddr` (unit).
- `make e2e-svc-caps` on tailnet 2: PASS with both grants, PASS with
  `TW_E2E_SVC_ONLY=1` (tag grant removed). Cleanup: 0 devices, 0 test keys,
  0 Services left; policy restored byte-identical to the backup (same ETag).
- `make lint`: ok. `make changes-check`: ok (no fragment needed).
- `make test` (mbp runner, before merging #221): exit 0, every package ok. After merging origin/master (#221: docs, mgmt/ui test): `make lint` ok, `go test ./docs/... ./cmd/tw -run Doc` ok.
- After the compatibility changes and merging origin/master (#223):
  `make lint` ok; `make test` (local, mbp slots busy) exit 0, no FAIL;
  `make changes-check` ok with the fragment.

Not run: `make e2e-svc` / `e2e-mm-svc` (tailnet 1, used by other agents).
