Last update: 2026-11-17 PT. Security docs match the strict macOS Seatbelt profile (follow-up to #121 / #171).

# Security docs after the strict macOS Seatbelt profile (#121)

PR #167 (#118) refreshed `security/` while the macOS profile was still
`(allow default)` plus a deny list; PR #171 (#121) then made it strict.
This branch brings the security docs in line with `hostd/seatbelt_strict.go`
at master adae38b. Docs only; no code change.

## What changed

- `security/THREAT-MODEL.md`: §3 macOS row rewritten from the profile
  (writes only own dirs and `/dev/null|zero|dtracehelper`; unix sockets
  only in own dirs; TCP connect only to `localhost:<port>` for the ports
  hostd passes, bind/accept only its own port; no UDP; signals only
  `same-sandbox`; exec only runtime, Node prefix, own dirs, `/System`,
  `/bin`, `/opt`, `/sbin`, `/usr`; Node egress `enforced`), with the
  residuals and the `seatbelt-legacy` fallback. ABI 1–3 row no longer says
  "same as macOS for TCP". The §3 summary and §4 goals 1, 2 and 7 no longer
  list macOS TCP/write/signal gaps as open; goal 7 cites the tests. Fixed
  an old inaccuracy in goal 1: the tailscaled-socket shared-host refusal is
  Linux-only (`procsb.ScanHostSockets` is a no-op elsewhere; on macOS the
  profile refuses the socket).
- `security/RED-TEAM.md`: a dated note at the end (earlier passes not
  rewritten, LOOP.md): RT-08 **holds on macOS** with the evidence tests;
  the two residual gaps.
- `security/LEDGER.md`: new claim C21 (macOS process tier confinement,
  pinned by `hostd.TestMultiTenant` live-profile probes,
  `TestNodeTier/Sandbox` in `make test-node`, `hostd.TestSeatbeltStrictProbes`,
  `hostd.TestSeatbeltStrictProfileShape`, `hostd.TestEgressEnforcementByABI`);
  new hypothesis H26 (open, Low) for the residuals, linked to issue #175
  (opened for it, `security` label); run log line.
- `docs/security-model.md`: the shared-host bullet on the tailscaled
  socket now says that under `-sandbox seatbelt-legacy` only sockets under
  the deny list are refused and hostd doesn't check for the tailscaled
  socket on macOS. The rest (macOS column, prose) was already updated by
  #171.
- Checked, no change needed: `docs/COMPAT.md`, `docs/node-tier.md`,
  `docs/INSTALL.md`, `README.md`.

## Still open (#175, H26)

- macOS reads are a deny list: outside it a workload reads what the user can.
- Seatbelt TCP rules match `localhost:<port>`, not one address.

## Tests

- `make lint`: PASS.
- `make test` (Go `-race` suites and JS): PASS, first run, no flaky reruns.
- `make changes-check`: ok (no upgrade-relevant paths; docs only, no fragment).
- Not run: `make test-node`, `e2e-mac` (no code change; #171 ran them).
