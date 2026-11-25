Last update: 2026-11-14 PT. Merged master (#13); fixed a cross-tenant sandbox regression from it; release workflow now builds the microVM guests on Linux runners.

Earlier: DX pass: strict twd.jsonc + schema, release workflow + get.sh, app-named tags, policy-doc fix.

# Developer-experience pass (branch `critique-developer-experience-admin-deployers`)

Started from a DX review of the admin path (install, `twd up`) and the
worker-developer path (`twd init`, `twd dev`, `twd deploy`). This branch has
the fixes that don't need new API surface. Remote `twd logs` and Worker
secrets are separate workstreams, each with its own status file.

## What's built

- **twd.jsonc is validated (`schema/`).** `schema/twd.schema.json` is the one
  list of twd.jsonc keys. The Go structs that read twd.jsonc (mgmt,
  hostd/bundle, placement, containers) each decode only part of it, so
  before this a typo was silently ignored. `schema.CheckTwd` implements the
  subset of JSON Schema the file uses and suggests the nearest key.
  `loadApp` (`twd dev`, `twd deploy`) refuses unknown keys or bad values:
  ```
  error [bad_request]: twd.jsonc has 2 problems
    tailnet.objcts: unknown key (did you mean "objects"?)
    tailnett_typo_ok: unknown key
  ```
  Before, `"objcts"` deployed every class as a plain DO with no node. Only
  the CLI enforces this; mgmt and hostd stay lenient, so bundles already
  stored keep loading.
- **`docs/twd-jsonc.md`:** a reference for every key. Templates now carry a
  `"$schema"` line for editor completion.
- **`twd init` names tags after the app.** `tag:twd-wl-<workload>` and
  `tag:twd-<workload>-<class>`; only the template's `tag:twd-…` tags are
  renamed. Before, two apps made from one template shared tags, and so
  shared grants. `--keep-tags` keeps the old names. The e2e suites and CLI
  tests pass it, because their fixed test policies own the template tags.
- **Bug fix:** `mgmt.ParseTwdConfig` blanked out the caller's bytes
  (`hujson.Standardize` works in place), so the `twd.jsonc` shipped in a
  bundle lost its comments. It now parses a copy.
- **Policy docs fixed.** INSTALL.md had `tag:twd-issuer` in the OAuth
  snippet; it belongs in the WIF one (`keylessSnippet`). The new
  `TestDocPolicySnippetsMatchCode` compares the three snippets in README.md
  and INSTALL.md with `operatorSnippet` / `keylessSnippet`.
- **Releases.**
  - `.github/workflows/release.yml`: on a `v*` tag (which must match
    `VERSION`), runs `make release` on macOS, smoke-tests the darwin
    tarball, and uploads the tarballs plus `SHA256SUMS` to a **draft**
    release.
  - Superseded (below): the workflow now builds every microVM guest on
    Linux runners.
  - `scripts/get.sh` downloads a release, verifies its hash and runs
    `install.sh`.
  - README step 1 now downloads a release; building from source is the
    fallback. README recommends all four OAuth scopes up front.

## Test results

- `make lint`: ok.
- `make test`: ok after the `--keep-tags` test fix. That includes
  `go test ./schema/`, which checks every `twd.jsonc` in the repo against
  the schema (12 files), and `TestAppTags` / `TestClassLabel` /
  `TestDocPolicySnippetsMatchCode` in `cmd/twd`.
- `make test-js`: ok.
- `scripts/get.sh` against a local HTTP mirror (`TWD_RELEASE_BASE`) and a
  real darwin tarball: installs into `TWD_HOME`; a wrong `SHA256SUMS`
  entry is refused with `checksum mismatch`.
- Not run: the release workflow itself (it needs a pushed tag on GitHub),
  and the e2e suites against a real tailnet.

## Release workflow: every component in CI

`.github/workflows/release.yml` now has four jobs:
- `vm-assets` runs `make vm-assets` on `ubuntu-24.04` (amd64) and
  `ubuntu-24.04-arm` (arm64), natively.
- `vz-assets` runs `make vz-assets` on arm64 Linux.
- `release` runs on macOS with all three asset dirs and
  `RELEASE_VM_ASSETS=1`.
- Before uploading, it checks that every tarball's `release.json` says
  `"microvm": true`.

Guest kernels and downloads are cached by their inputs. None of the guest
builds need root or a VM.

v0.1.0's first tag push started no run: GitHub hadn't registered the
workflow, because it wasn't on the default branch yet. That draft release
was built by hand (`make release RELEASE_VERSION=0.1.0`, Mac + Lima, with
all guests) and checked with `get.sh` from the downloaded draft assets.

## Merge of master (#13): fixes the merge needed

- **Cross-tenant read on master.** M3 limited a host's `GET /v1/tenants`
  to the tenants it runs, but hostd's `sandboxDeny()` used that list to
  deny every tenant's credential files. So a workload started before
  another tenant's first deploy could read that tenant's API key file;
  `TestCLIMultiTenant` (d) failed on master. New `GET
  /v1/hosts/{id}/sandbox-deny` returns paths only (no tenant IDs), and
  hostd uses it. Covered by `TestHostSandboxDeny`; `TestCLIMultiTenant`
  passes.
- `GET /v1/logs` now sits in `pushHandlers`, behind master's `pushAuth`.
  The merge had left it on the local socket, which would have broken
  remote `twd logs`. In-process hosts (`inproc://`) are served through a
  streaming handler transport (`mgmt/logs_inproc.go`). The branch's own
  WhoIs check is gone, because pushAuth covers it.
- Master's `commandHelp` replaces `helpflags.go`.
- The worker-secrets ADR is numbered ADR-034.

## E2E run (2026-11-14, this branch)

Real-tailnet suites ran one at a time on tailnet 1; hermetic suites ran
alongside them, and the Firecracker suites ran inside Lima twd-dev on a
copy of this tree. `mbp` was unreachable, so `e2e-mbp` didn't run.

| suite | result |
|---|---|
| e2e-placement, e2e-local, e2e-coldid, test-node, test-sdk, test-images, test-pg | PASS |
| e2e-vz (microVM tier on macOS) | PASS |
| e2e-vm (Firecracker, Lima) | PASS (46 tests) |
| e2e-container (Firecracker, Lima) | PASS (22) after the stale-check fix below |
| e2e-mac, e2e-oauth, e2e-svc, e2e-coldid-real (tailnet 1) | PASS |
| e2e-multi (tailnets 1 + 2) | PASS after the teardown gate; failed on master too |
| e2e-linux (4 fresh Lima VMs) | debian12, ubuntu22, ubuntu24-amd64 PASS; ubuntu24-arm64 FAIL (pool node not restored at reboot, below) |
| e2e-linux-reboot | PASS |
| e2e-linux-vm | FAIL: one pool device left after down (below) |
| e2e-real (TestMgmtRealWIF), e2e-keyless, e2e-public | FAIL: the issuer/public URL waited 3 to 12 min for its HTTPS certificate (see below) |

### Fixed here

- **Landlock did nothing on ABI 1 (`internal/procsb`).** The own-dirs rule
  asked for `refer`, which only exists from ABI 2. go-landlock's BestEffort
  mode then dropped the whole ruleset ("ABI V0"), so on Ubuntu 22.04 (kernel
  5.15) and any 5.13 to 5.18 kernel, workloads had no filesystem sandbox,
  only seccomp. `twd doctor` failed `sandbox` there, and STATUS.md noted that
  ABI 1 "has no measured row yet". Now `refer` is requested only on
  ABI >= 2. Verified on a fresh Ubuntu 22.04 VM: doctor reports the sandbox
  enforcing, and the e2e-linux ubuntu22 row passes.
- **`twd down --purge` left devices behind.** down listed the objects, killed
  them, then stopped the daemons. Two things created objects in between, and
  their devices stayed on the tailnet:
  - pool refills (killing a pool node topped the pool up again), seen in
    e2e-multi, which leaked one pool device per tailnet on master as well;
  - public creates over Funnel, seen in e2e-public as `-open-` devices.

  `POST /v1/teardown` (`hostd/teardown.go`) refuses every new object in
  `h.create`, the single entry point, and waits for creates already in
  flight. down calls it before listing. Covered by `TestTeardownGate`;
  e2e-multi now passes with 0 untracked devices on both tailnets.
- **e2e/linux cleanup** never deleted mgmt's and the host's own nodes. Every
  other suite deletes `InfraNodeIDs` by ID; this one left 2 devices per row.
- **e2e/container `PortsAndAttribution`** still expected the forge attempt
  to return `ok: false`. Since create admission, the platform binding isn't
  in tenant env at all, so the answer is `noBinding`, which hostd's
  `TestContainers` already expects. The e2e copy failed on master too.

### Open (all older than this branch)

- **Idle pool node not restored after a reboot.** In e2e-linux
  ubuntu24-arm64, `pool-qide2dd5cb` was ready before the reboot and never
  rejoined afterwards: no `object node ready` line at either restart.
  mgmt still listed it as `ready` with its old node ID. ADR-031 says "a
  host restart re-uses pool nodes from saved state". The test's rejoin
  count (3 of 4) is right to flag it.
- **e2e-linux-vm still leaves one pool device** after down, even with the
  teardown gate. The node was created about 20 s before down and was
  online until the daemons stopped, so it was in down's list. Either it
  wasn't killed or its device wasn't deleted. The suite deletes its VM
  state in cleanup, so the next step is a rerun that keeps the hostd/mgmt
  logs. It's probably related to the redeploy or VM-kill steps that only
  this suite has.
- **The pool-node `object node ready` log** says `rejoined=false` for an
  object assigned from the pool (`hostd/poolnodes.go`), which is misleading
  next to the regular rejoin line.
- **The Funnel suites (TestMgmtRealWIF, e2e-keyless, e2e-public) are blocked
  by the environment, not this branch.** Each one waits for a brand-new
  hostname's HTTPS certificate. Today tsnet's
  `localapi/v0/cert/<name>` timed out (`context deadline exceeded`), or
  the certificate took 9 to 12 minutes, which is past the tests' limits.
  Unmodified `origin/master` fails `TestMgmtRealWIF` the same way: it
  passed 1 of 2 runs this evening, with the identical TLS handshake error.
  The branch doesn't touch tsnet, the certs, the issuer or go.mod. Rerun
  all three when issuance is back to its usual 1 to 2 minutes.

All 15 test devices leaked by today's runs (pool, `-open-`, the e2e-linux
infra nodes) were deleted by ID on tailnets 1 and 2.

## CLI polish

- `twd <cmd> --help` lists each flag with its description and default
  (`cmd/twd/helpflags.go`). Help mode runs the command only up to its flag
  parsing; every command parses before it does anything.
  `TestHelpListsFlags` checks that `twd init x --help` creates nothing.
  (`--help` already exited 0. The review's "exits 1" came from a failed
  shell glob in the same command, not from twd.)
- `twd dev` drops tailscale stunserver's `STUN server listening on …`
  line, which went through the global logger (`cmd/twd/devlog.go`).
- `twd dev` checks `--port` before it bundles and starts the control
  plane. The error names `wrangler dev`, which also uses 8787. It warns
  when `[::1]:<port>` belongs to another server, because then `localhost`
  reaches that server. The default stays 8787, as in wrangler.

## Worker secrets: node tier on macOS

The secrets branch passed node-tier secrets as environment variables. On
macOS any process of the same user can read another process's exec-time
environment with `sysctl KERN_PROCARGS2`. I tested this with a canary
process: an `(allow default)` Seatbelt profile like twd's doesn't block
the read. Neither `(deny process-info* (target others))` nor
`sysctl-read` name filters stop it, because the call is MIB-based. The
macOS process tier runs every tenant as one user, so another tenant's
sandboxed Node app could read them.

Fix (`hostd/secrets.go`, `writeNodeSecrets`): node secrets go into a
`0600` file in the instance dir. Node starts with `--require` of a preload
that copies them into `process.env` and deletes the file; every (re)start
writes it again. `TestNodeSecrets`, with real node and
`make test-node` deps, now also checks that the value is not in the
process's exec-time environment (`procenv_{darwin,linux}_test.go`, with
a positive control on `TWD_WORKLOAD=`) and that the file is gone after
start.

**Still open (older than this branch):** the node tier also passes
`TWD_CALLER_KEY` and the egress proxy's credentials (`HTTP_PROXY` /
`TWD_EGRESS_PROXY`, with a token) in the environment. On macOS, the
same read lets another same-user process forge caller headers to that
app or send egress through its proxy as that workload. Node reads the
proxy variables at bootstrap, so moving them changes how egress is
enforced and needs its own change and review. On Linux, Landlock hides
other processes' `/proc` entries.

## Remote logs: caller check on hostd

The push API (tcp:7474 on the host's node) relied only on the policy grant
`tag:twd-mgmt → tag:twd-host`. Logs are tenant data: with a broad grant
(for example the default allow-all policy), any node could read any
workload's log and skip mgmt's admin check. `GET /v1/logs` now asks WhoIs
(`hostd.Config.PushCallerTags`, `cmd/twd-hostd/pushcaller.go`) and answers
TCP callers only if they carry `tag:twd-mgmt`. A failed WhoIs is refused,
and the unix socket is unchanged. Test: `TestLogsHandlerCallerMustBeMgmt`.
After the merge, `make lint test` passes; `TestCLILink` ran here (not
skipped) and covers `twd logs` from a linked machine.

Kills, `/v1/status` and the other push routes still have no such check.
Extending it to the whole push API needs the e2e suites run against a
real tailnet first.

## Gaps and next steps

- No release is published yet, so `get.sh` reports "no published
  release" until a maintainer pushes a `v*` tag and publishes the draft.
- The schema has no `maximum`. mgmt still enforces limits such as
  `replicas` ≤ 16.
