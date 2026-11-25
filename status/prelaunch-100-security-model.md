Last update: 2026-11-16 PT. Issue #100: security model matches the code.

## What changed

- `docs/security-model.md`: the process-tier isolation section is now a
  per-platform table (macOS Seatbelt; Linux Landlock ABI ≥ 4; ABI 1–3),
  traced to `hostd/seatbelt.go` (`seatbeltProfile`, `sandboxDeny`,
  `workerdCommand`, `nodeCommand`) and `internal/procsb` (`Rules`,
  `buildFilter`). States plainly that Seatbelt is `(allow default)` plus a
  deny list (TCP, signals and writes outside the deny list are not
  confined) and that TCP is unrestricted below Landlock ABI 4. New
  sections: "When a host is shared between tenants" (what `sharedHostCheck`
  and the `egress_unenforced` rule actually refuse; ABI 1–3 and macOS hosts
  are admitted, with a warning) and "Who you trust" (host operator, mgmt
  operator, tailnet admin). Removed the WIF/API-key mint text (ADR-042).
  Known gaps now states the in-process `request.cf` caveat (C04/C05, H08).
- `hostd.EgressEnforcement` consults the Landlock ABI: `enforced` only for
  Landlock ABI ≥ 4. `tw status`, `/v1/status` and doctor's `node_egress`
  check all read this value, so an ABI 1–3 host now says `egress
  unenforced (… cooperative …)`. Helper in `hostd/seatbelt.go`; unit test
  `hostd.TestEgressEnforcementByABI`.
- Shared-host admission on ABI < 4: decided to keep it as on macOS
  (admitted; status and doctor say unenforced), and documented it. Refusing
  would also have to cover macOS, which is a separate behaviour change.
- netd strips client `tailscale-*`, `x_tw_*` and `tw_*` headers by prefix
  (ledger F11 strip part, H18). Test `netd.TestRT3_H18_StripByPrefix`.
  LEDGER H18 fixed, C06 resolved, F11 strip part done.
- Aligned: COMPAT.md (Node tier caveat no longer claims a multi-tenant
  host refuses the deploy), node-tier.md, INSTALL.md "Isolation" (the
  "write only its own dirs" claim is Linux-only), RED-TEAM.md RT-08 row.
- `e2e/node` assertion: Landlock no longer implies `enforced`.

Out of scope: tightening the Seatbelt profile (#121).

## Tests

- `make lint`: ok.
- `make changes-check`: ok (no upgrade-relevant paths).
- `make test` (macOS): all packages ok except two hostd tests on the
  first run: `TestWorkersKVMove` (data race in `hostd/host.go:403` /
  `objects.go:454`, code this branch doesn't touch) and
  `TestLockTemplate/KillHolderReleases` (timing). Both pass on rerun:
  `go test -race ./hostd -run 'TestWorkersKVMove|TestLockTemplate'` ok.
- New tests: `hostd.TestEgressEnforcementByABI`,
  `netd.TestRT3_H18_StripByPrefix`: pass.
- `GOOS=linux go vet ./hostd ./netd`, `go vet -tags nodetier ./e2e/node`: ok.
- Not run: `make test-node` and the Linux Landlock e2e (need Linux with
  Landlock; no ABI 1–3 kernel here).
