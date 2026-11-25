Last update: 2026-11-17 PT. Strict macOS Seatbelt profile (#121): writes, network, signals and exec confined; Node egress enforced on macOS.

# Tighten the macOS Seatbelt profile (#121)

## What changed

- `hostd/seatbelt_strict.go`: the new `seatbeltProfile`. On top of
  `(allow default)` (kept for Mach services and sysctls):
  - `(deny file-write*)`, then writes allowed back only in the workload's
    own dirs and `/dev/null`, `/dev/zero`, `/dev/dtracehelper`.
  - `(deny network*)`, then: unix sockets (connect, bind, accept) in own
    dirs; TCP connect to `localhost:<port>` for the ports hostd passes
    (`connectTCP`: a Node app's egress proxy and its own port; workerd
    gets none on macOS, its services are unix sockets); TCP bind/accept on
    `localhost:<port>` for `bindTCP` (a Node app's own port, workerd's
    inspector). No UDP, so no DNS of its own (netd resolves names).
  - `(deny signal)` + `(allow signal (target same-sandbox))`: itself and its
    children only. Measured: two separate `sandbox-exec` instances with the
    same profile can't signal each other, so tenants and workloads are apart.
  - `(deny process-exec*)`, then exec of the runtime binary, the Node install
    prefix, own dirs (parity with Linux, where own dirs are RWX) and
    `/System`, `/bin`, `/opt`, `/sbin`, `/usr`.
  - The read deny list (home dir, hostd data/run dirs, credential files) and
    the read allows are unchanged.
- `hostd/seatbelt_legacy.go`: the previous profile, kept for one release
  behind `tw-hostd -sandbox seatbelt-legacy` (profile file
  `sandbox-legacy.sb`, reported as sandbox `seatbelt-legacy`, egress
  `unenforced`).
- `EgressEnforcement("seatbelt")` is now `enforced` (Node tier on macOS).
- `tw doctor` sandbox check (macOS) also probes that a write outside the
  workload's dirs and a signal to another process are refused; detail text
  updated.
- Docs: macOS column of `docs/security-model.md` "What the process-tier
  sandbox confines", the macOS sentences in `docs/node-tier.md`,
  `docs/COMPAT.md` (Node tier) and `docs/INSTALL.md` (Isolation).
- nodeprobe fixture: `/signal?pid=` probe (kill(pid, 0)).

## Tests (macOS 27.2, this Mac)

- New `hostd/seatbelt_strict_test.go`: `TestSeatbeltStrictProfileShape`,
  and `TestSeatbeltStrictProbes`, which runs real probes under
  `sandbox-exec`. Each denied probe is first run unsandboxed and must
  succeed there. Denied: TCP connect to a loopback port not given, TCP to
  1.1.1.1:443, TCP bind of another port, `kill -0` of another process and
  of the test process (hostd), write outside own dir, write to `/tmp`,
  read of a denied dir, exec of a script outside the allowed dirs, unix
  connect to a socket outside own dirs. Allowed: TCP connect to the given
  port, bind of its own port, signal self and a child, write own dir and
  `/dev/null`, exec from own dir, unix socket in own dir, `workerd
  --version`, node listening on its port and connecting to the "proxy".
  PASS.
- `hostd/seatbelt_live_test.go` + `TestMultiTenant`: under tenant A's live
  workerd profile, loopback TCP, `kill -0` hostd and a `/tmp` write fail;
  own instance dir write works. PASS.
- `make test-node`: new `seatbeltNodeChecks` (e2e/node/seatbelt_test.go)
  from inside the nodeprobe app: `tcp_1.1.1.1:443`, `tcp_host_loopback`,
  `unix_hostd_push_a`, `signal_hostd`, `write_tmp` all `EPERM`; own port and
  signalling itself work; status `Egress: enforced`. TestNodeWorkflow
  (Vercel Workflow, restarts) passes under the strict profile. PASS.
- `make lint`: PASS. `TestDoctor` (cmd/tw): PASS.
- `make test` (as its parts, after merging origin/master): `go test -race` of netd/... and internal/... PASS; of cmd, hostd, security, mgmt, docs, schema, guest, agent PASS; `make test-js` PASS. `make test-sdk` PASS (all SDK apps under seatbelt). The single `make test` run hit the 30-min tool limit (~14
  agents running suites at once, load average 35-47), so it was run in parts.

## Gaps / next

- Seatbelt TCP rules match `localhost:<port>`, not specific addresses; a
  workload may reach those ports on any loopback address only.
- Exec from `/opt` and `/usr` is allowed (Homebrew tools, `/bin/sh`); the
  children stay in the same sandbox.
- `make e2e-mac` / `e2e-mbp` (real tailnet) not run here; see the PR.
- security/THREAT-MODEL.md and security/LEDGER.md (owned by #118) should
  record the macOS rows as closed: TCP pinned to the egress proxy and own
  port, signals limited to same-sandbox, writes limited to own dirs, RT-08
  enforced on macOS.
- Drop `seatbelt-legacy` one release after this ships.
