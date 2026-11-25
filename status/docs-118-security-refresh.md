Last update: 2026-11-17 PT. Security docs refresh (#118): THREAT-MODEL revised, ledger audited, open rows filed as issues, claims pinned at the wiring.

# docs-118-security-refresh

Issue #118. Everything below was checked against master e62f70a (after
#127 Host allowlist, #129 security-model accuracy, #130 audit snapshot,
#131 license notices, #132 upgrade signature check).

## What changed

- `security/THREAT-MODEL.md`: dated 2026-11-08 with a "Revised: 2026-11-17"
  line. §3 tiers rewritten to match `docs/security-model.md`: macOS Seatbelt
  (allow default + deny list: TCP, `/tmp` writes and same-user signals open,
  #121), Linux Landlock ABI ≥ 4 (files, AF_UNIX, TCP confined), ABI 1–3
  (TCP open), no Landlock (single tenant, `sandbox_unavailable`), microVM
  on Linux (jailer) and macOS. The old text said Linux process/Node had no
  confinement. New boundaries B7 `/mcp`, B8 public ingress, B9 release ↔
  host (signatures), B10 hosts' push API; B6 (admin's browser) kept. §4
  restates each attacker goal with what is fixed and the open issue.
  Dead references removed (`review/security.md`, branch `rt-redteam`,
  "M0–M9 in STATUS.md").
- `security/LEDGER.md`: H15 fixed (`mgmt.TestAuthzM1RegistrationSecret`,
  `mgmt.TestTailnetAuth`), H20 fixed (`attacks.TestRT2L1_…`, not skipped;
  `policy.IsTwTag`). Also found fixed and marked: H14 (host read scope,
  `mgmt.TestAuthzM3HostReadScope`), F10 and F11 done. Every open row links
  its issue (#148–#158). Claims C09–C17 and C20 name wiring-level tests;
  C20 used to cite `bundle_test.go`, which doesn't exist. Header and run
  log updated.
- `security/LOOP.md`: hard rule 8 (status changes only with a test name or
  an issue link; pinned means the production path), the iteration step
  opens a `security` issue, the claims auditor reads `status/*.md` and the
  "Pinned by" column.
- `docs/security-model.md`: trust-boundary rows for `/mcp` and release ↔
  host; Known gaps says each ledger item has an issue. The macOS rows were
  not touched (#121 owns them).
- `changes/docs-118-security-refresh.md` (kind security, docs only).

## Issues opened (label `security`)

| Ledger | Issue |
|---|---|
| H08 / F5 in-process `request.cf` sealing | #148 |
| H11 / F8 uffd region validation | #149 |
| H13 / F9 terminal escapes | #150 |
| H16 / F8 vmhelper base guard | #151 |
| H17 / F12 cron overlap | #152 |
| H19 / F12 admit cache after kill | #153 |
| H21 once-decoded public path | #154 |
| H22 `svc:` caps in CapMap | #155 |
| H23 placement poisoning | #156 |
| H24 snapshot shared kernel state | #157 |
| H25 warm keys in custody | #158 |

Not filed: H14 (fixed by M3), H15 (fixed by M1), H18 (fixed by F11 in
#100; only `Via` passes, not an identity header), H20 (fixed).

## New tests

| Claim | Test | What it drives |
|---|---|---|
| C10 | `internal/mgmtd.TestUnixSocketNeedsHostToken` | `mgmtd.Start` with `LocalDev`: the socket tw-mgmt and `tw up` serve, the 0600 token file it writes; `/v1/healthz`, `/v1/tenants`, `/v1/hosts`, `/v1/audit`, `/mcp` refuse no token (401) and a wrong one (403) |
| C11 | `hostd.TestEgressMACReplayRefusedByHandler` | `egressObjHandler` with the instance's nonce cache: replay, out-of-skew and another workload's key refused |
| C13 | `netd.TestPublicPathTricksNeverReachPlatformPaths` | 17 encoding/normalisation tricks under a `/*` route; nothing reaches the app at `/_tw` |
| C17 | `hostd/gen.TestEgressSink` (fixture `hostd/testdata/apps/egresssink`) | generated config + real workerd; Worker and facet object try fetch, POST, Host override, followed redirect, WebSocket upgrade and constructor, `connect()` (plain and starttls), Cache API, `node:net`, `node:http` at a loopback sink; the sink sees 0 connections, fetch and redirect arrive at the platform egress attributed to the caller |
| C20 | `hostd/bundle.TestUnpackRefusesHostileTars`, `hostd/gen.TestCapnpConfigNotInjectable` | `bundle.Unpack` (hostd runs it on every bundle): traversal, absolute, backslash, quote/CR/LF names, symlink, hardlink, device, fifo, oversize; var values can't add a capnp binding, bad var names refused |

`hostd/gen/gen_test.go`: the fake egress handlers answer
`/__redirect?to=` with a 302 (two one-line calls to `egressRedirect`, which
lives in `egress_sink_test.go`).

## Findings

- H14 and H15 were fixed by M3/M1 in 5b9e955, the same day Pass 3 seeded
  the ledger, and never marked. H20 was fixed by `IsTwTag` in the rename
  (a53240b).
- `TestEgressSink` log: `connect()` and `node:net` fail inside workerd
  ("proxy request failed"); the Cache API has no cache configured;
  WebSocket upgrades go to the egress socket. No path reached the sink.
- `TestPublicPathTricks…` log: `/_tw%00/o` reaches the app with a NUL in
  the path (served by the workload's default export, not the platform).
  Added to #150 (H13): control bytes pass `CanonicalPath`.

## Tests run

- `go test ./security/attacks/ -run 'TestRT2L1|TestRT0'`: pass (TestRT2L1 runs, not skipped).
- `go test ./mgmt/ -run 'TestTailnetAuth$'`: pass.
- Each new test alone: pass.
- `make lint`: pass (changes-check included).
- `make test`: everything passed except `hostd.TestWorkersKVMove`, a data
  race in `mgmtapi.Client.setHeaders` (known flake #135, unrelated). A
  rerun of `go test -race -count=1 -run 'TestWorkersKVMove|TestEgressMACReplayRefusedByHandler|TestTenantAuditIsOnlyANote|TestPublicIngress' ./hostd/`
  passed. The new tests passed in the full run (netd, internal/mgmtd,
  hostd/bundle, hostd/gen, security/attacks).
- After merging origin/master (9d3db9c: npm publishing docs and workflow
  only, no security-relevant change): `make lint` pass.
- After merging origin/master again (544caa4, #164 tenant purge for #107):
  `tw tenant rm` now revokes a tenant's warm keys, so the ledger's H25 row
  and #158 say so. `make lint` pass; `make test` pass (all packages).
- After merging #160 (#135 race fix) and #162 (#117: a Worker's fetch to an
  internet name now fails `internet_egress_unavailable`; still no internet
  egress for Workers): no claim here changes; THREAT-MODEL already says
  Worker egress is netd's guard to tailnet peers only. `make lint` and
  `make test` rerun after each merge.

## Gaps

- C15 and C16 are pinned by KVM suites (`make e2e-vm`) that CI doesn't run;
  not run here (macOS).
- `RED-TEAM.md` Pass 1/2 text is left as written (LOOP.md: never rewrite
  earlier passes); THREAT-MODEL.md is the current view.
