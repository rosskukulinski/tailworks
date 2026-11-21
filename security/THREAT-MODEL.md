# tailworks Threat Model (red-team)

Date: 2026-11-08 (first version, red-team Pass 1).
Revised: 2026-11-17 (#118): tiers and sandboxes match the code at master
e62f70a; boundaries B7–B10 added for `/mcp`, public ingress, upgrades and
hosts' push API; attacker goals restated with what is fixed and what is
open.
Revised: 2026-11-17 (follow-up to #121): the macOS process tier matches the
strict Seatbelt profile at master adae38b (#171).

Companions: [`docs/security-model.md`](../docs/security-model.md) (the
user-facing model; where this file and that one disagree, fix both),
[`RED-TEAM.md`](RED-TEAM.md) (findings, by pass),
[`LEDGER.md`](LEDGER.md) (open hypotheses, each with an issue, and the
claims register with the test that pins each claim), and
[`attacks/`](attacks/) (regression tests).

This is the attacker's view. It is built from the design
([`docs/design/RFC.md`](../docs/design/RFC.md) and its ADRs) and checked
against the implementation (netd, mgmt, hostd, the platform runtime and
the generated workerd config). Where the design and the code disagree, the
code wins.

## 1. Assets (what an attacker wants)

| # | Asset | Where it lives |
|---|-------|----------------|
| A1 | Per-object tailnet **node keys** / machine keys | hostd's 0700 state dirs, one `tsnet.Server` per object (netd); never inside a microVM guest |
| A2 | Tenant **mint credentials** (OAuth client secret; Headscale API key) | tenant `oauthClientFile` / `apiKeyFile` paths (0600); mgmt reads them at use time |
| A3 | mgmt **custody key** + custody secrets (sealed Worker secrets, microVM volume keys, warm keys) | `custody.key` (0600) in mgmt's state dir |
| A4 | **Caller identity** (`request.cf.tailscale.caller`, Node `x-tw-caller`) | set by netd/hostd from WhoIs |
| A5 | **Object egress identity** (which node a `fetch()` leaves from) | platform `Egress` + per-object MAC + netd dialer |
| A6 | **Capabilities / delegation** (grants' capMap, spawn `lim`) | tailnet policy + mgmt spawn checks + netd at the receiver |
| A7 | **Control surfaces**: mgmt's unix socket (per-host token), mgmt's `/v1`, `/mcp` and `/ui` on its tailnet node, hostd's push API, an instance's `hostapi.sock` and egress sockets | unix sockets 0600 in 0700 dirs; tailnet listeners behind WhoIs |
| A8 | **Audit log** (hash-chained; host records signed with the host's pinned ed25519 key) | mgmt store; `tw audit` |
| A9 | **Budgets / leases** (epoch fencing, `maxObjects`, registration throttle, public limits) | mgmt store; netd |
| A10 | **Worker secrets and Durable Object data** | custody (sealed), the 0700 instance dir (plaintext while deployed), DO storage dirs, a microVM's encrypted volume |
| A11 | **What a host installs** (release tarballs) | GitHub releases; mgmt's release cache; verified against release keys pinned in each binary |

## 2. Trust boundaries

```
 tailnet client ──(WhoIs)── netd ingress ── hostd ── platform wrapper ──┐
 internet ──(Funnel, tag:tw-public)── netd public ──┘                    ├─ tenant isolate / process / VM (HOSTILE)
 tenant code ── globalOutbound (platform Egress + MAC) ── hostd ── netd egress (ADR-019) ── tailnet
                         │                                   │
                   hostapi.sock                  mgmt: unix socket (token) · /v1 · /mcp · /ui (WhoIs)
                                                     │              │
                                     Tailscale control API    admin's browser / agents
```

- **B1 Tenant code ↔ platform.** The sharpest boundary. Tenant code
  (workerd isolate, Node process, or microVM guest) is fully hostile (for
  example a prompt-injected agent). It must not reach A1–A3/A7 or forge
  A4–A6. Held by the tier's sandbox or VM (§3), the platform wrapper
  (props, `request.cf`, create admission), and netd's egress guard.
- **B2 Tenant ↔ tenant** on one host. One hostd process, one uid,
  per-tenant netd registries (one tailnet's netmap each), per-workload
  sandboxes, 0700 state dirs. A host without a working sandbox runs one
  tenant's process-tier workloads only (§3).
- **B3 Object ↔ object** within a workload. Per-object nodes and the
  per-object egress MAC bind attribution; spawn `lim` narrows a child at
  the receiver. Code inside one workload trusts the rest of that workload:
  `request.cf` is not sealed in-process (H08, #148).
- **B4 Host ↔ mgmt.** A host is `tag:tw-host` by WhoIs, bound to its host
  ID by a one-time registration secret (`tw host add`), and trusted only
  for the leases it holds: every host write is fenced by lease holder and
  epoch, and host reads are scoped to its tenants (F1, M1, M3). On the
  local machine, mgmt's unix socket needs the per-host token; same-uid
  peercred alone is not enough (RT-03).
- **B5 Platform ↔ Tailscale control.** One revocable mint credential per
  tenant (OAuth client, or Headscale API key). Object keys are single use,
  pre-authorized, tagged, and expire within 5 minutes; warm keys wait in
  custody for up to 30 minutes.
- **B6 Admin's browser ↔ mgmt.** An admin's browser is on the tailnet and
  carries the admin's WhoIs identity to mgmt's `/ui` and `/v1` (plain HTTP
  on :80, HTTPS on :443 when the tailnet has certificates). Any web page
  the admin opens runs in that browser. A cross-origin page must not drive
  mgmt as the admin (CSRF), and neither may a page on a DNS name rebound to
  mgmt's tailnet IP, which is same-origin with mgmt. Held by the Host
  allowlist on every tailnet listener (`mgmt/hostguard.go`: mgmt's own
  names or an IP literal, every method, before WhoIs; 421
  `misdirected_request`) and `browserGuard` (`Sec-Fetch-Site`, `Origin`
  equal to `Host` and on the allowlist, `X-Tw-UI` for `/v1` mutations). The
  UI holds no cookies or tokens ([ADMIN-UI.md](../docs/ADMIN-UI.md)).
- **B7 Agents ↔ mgmt (`/mcp`).** tailworks' MCP server is `POST /mcp` on
  the same listeners and behind the same WhoIs gate as `/v1`
  (`mgmt/mcp.go`). A caller holding only `…/cap/mcp` reaches `/mcp` and
  nothing else; read tools need that cap or the admin cap; deploy, kill
  and undeploy need a grant that names them (being an admin is not
  enough). Tools run the `/v1` handlers in process, so roles, validation
  and audit are the API's. On the local socket the `tw mcp` bridge gets
  the read tools unless the operator passes `--allow`. Hosts get nothing.
  ([agents.md](../docs/agents.md)).
- **B8 Internet ↔ workload (public ingress).** Tailnet-only by default.
  A workload that lists public routes gets one more node, tagged
  `tag:tw-public`, served over Funnel (ADR-029,
  [public-ingress.md](../docs/public-ingress.md)). netd serves only the
  listed path prefixes, on the once-decoded path, refuses `/_tw` and
  traversal tricks, strips identity headers by prefix, and applies per-IP
  and per-node limits. The platform hands a public request to the
  workload's default export only, with `getCaller()` throwing
  `public_request`. A public request creates an object only through
  `admit` under `public.create` with `maxObjects` (I2). A public name
  belongs to one tenant per tailnet (F7). mgmt's own node is never
  Funnel-capable.
- **B9 Release ↔ host (upgrades).** `get.sh` and `install.sh`, `tw
  upgrade` on a `tw up` machine, and hostd's self-upgrade (opt-in,
  `tw-hostd -self-upgrade`) all verify the release's `SHA256SUMS`
  signature against release keys pinned in the binary or script
  (`internal/releasesig`), never keys the release or tw-mgmt supplies.
  The SHA-256 in mgmt's upgrade order proves only that the download
  matches what mgmt holds: a compromised mgmt can order an upgrade but
  can't sign a release. `tw upgrade --allow-unsigned` is the operator's
  explicit override for a build they made ([upgrading.md](../docs/upgrading.md)).
- **B10 mgmt → host (push API).** mgmt pushes kills, assignments, path
  probes and upgrade orders to `POST /v1/push/*` on the host. On the
  tailnet the push API admits only mgmt's node (`tag:tw-mgmt`, pinned at
  start; `hostd/pushauth.go`).

## 3. Attacker capabilities per tier

What tenant code can do on the process tier depends on the OS sandbox
`tw-hostd -sandbox auto` resolves to. This table matches
[docs/security-model.md](../docs/security-model.md#what-the-process-tier-sandbox-confines),
which has the full per-platform rows.

| Tier | Native code? | Isolation | Residual attacker power |
|------|-------------|-----------|-------------------------|
| **process, macOS** (Seatbelt) | Node app: yes. workerd: only after a V8 escape | V8 isolate (workerd); Seatbelt `(allow default)` for system services (Mach, sysctls), with writes, network, signals and exec denied and allowed back for what the workload needs (`seatbeltProfile`, `hostd/seatbelt_strict.go`) | Can't read or write the home dir, tailworks' state and run dirs or other tenants' credential files. Writes only its own dirs and `/dev/null`, `/dev/zero`, `/dev/dtracehelper`. Network: unix sockets only in its own dirs (not tailworks' sockets or the system tailscaled socket); TCP connect only to `localhost:<port>` for the ports hostd passes (a Node app: its egress proxy and its own port; workerd: none), bind and accept only its own port; no UDP, so no DNS of its own. Signals only itself and its children (`same-sandbox`): not tw-hostd, tw-mgmt or another workload. Exec only of the runtime, the Node prefix, its own dirs and `/System`, `/bin`, `/opt`, `/sbin`, `/usr`. Node-tier egress is `enforced`. **Residual** ([#175](https://github.com/rosskukulinski/tailworks/issues/175), H26): reads outside the deny list (`/etc`, `/tmp`, …) stay open, and TCP is matched as `localhost:<port>` rather than one address. `-sandbox seatbelt-legacy` (kept for one release) brings back the old files-only profile: TCP anywhere, writes to `/tmp`, signals to any same-user process, unix sockets outside the deny list (including the system tailscaled socket), egress `unenforced`. |
| **process, Linux, Landlock ABI ≥ 4** (kernel 6.7+) | as above | Landlock allow list + seccomp (`internal/procsb`) | Reads only OS dirs, its runtime and its own dirs; writes only its own dirs; TCP connect only to the ports hostd gives it; no AF_UNIX (seccomp), no UDP/raw; signals only its own process group; no ptrace, mount, bpf, namespaces, io_uring. Residual: the kernel's attack surface, and the same uid as tailworks (an escape from the sandbox is the host operator). |
| **process, Linux, Landlock ABI 1–3** | as above | as above, but Landlock has no TCP rules | As ABI ≥ 4 for files, unix sockets, signals and syscalls. TCP is **unrestricted** (loopback, including other tenants' ports; the LAN; the internet); Node-tier egress `unenforced`. |
| **process, Linux without Landlock** (or `-sandbox none`) | as above | none | Full same-uid access: every tenant's node keys and state, the mgmt token file, every control socket. hostd therefore runs one tenant's process-tier workloads only (`sandbox_unavailable` for a second), unless started with `--insecure-shared-host`. |
| **microVM, Linux** (Firecracker) | yes, inside the guest | a VM per workload; Firecracker under its jailer (chroot, uid, netns, cgroup); per-VM gateway; node keys never in the guest; DO storage on a dm-crypt volume whose key mgmt releases only to the lease holder | Confined to its own workload. Holds its workload's MAC root, so it can forge egress attribution **among its own workload's objects**. After a VMM escape: the jail uid, and the root vmhelper's message surface (H11 #149, H16 #151). Snapshot-restored guests share kernel state from before the reseed (H24 #157). |
| **microVM, macOS** (Virtualization.framework, Apple silicon) | yes, inside the guest | a VM per workload; node keys stay on the host | Confined to its own workload; forges attribution only among its own workload's objects, as above. |

On every tier the workload runs as tailworks' user (or the jail uid) and
the process tier sets no CPU or memory limit (#115). The Node runtime is
process tier only.

So: the microVM tier is the boundary that doesn't depend on the host
kernel's same-uid sandbox. On Linux with Landlock ABI ≥ 4 and on macOS the
process tier confines files, sockets and TCP (on macOS, reads only under
a deny list); on Landlock ABI 1–3 (and macOS under
`-sandbox seatbelt-legacy`) it confines files and unix sockets but not TCP;
without Landlock it confines nothing and the host is single-tenant. hostd's
shared-host checks refuse the unconfined cases (§4.1); a host that confines
files but not TCP is admitted as shared, and `tw status`/`tw doctor` say
so.

## 4. Attacker goals, and where each stands

1. **Tenant → tenant** (B2): read A1/A2/A3, forge A5 to another object,
   reach another tenant's objects. → netd's netmap isolation holds (one
   registry per tailnet). Files and unix sockets are confined on macOS and
   on any Linux with Landlock; TCP to another tenant's loopback ports and
   signals to other processes are refused on macOS (#121, fixed by #171)
   and on Landlock ABI ≥ 4. A second tenant is refused on a host with no
   sandbox, or whose user can reach a container engine, or (on Linux,
   with a Node app in `listen: "unix"` mode) can reach the system
   tailscaled socket.
   **Open:** on Landlock ABI 1–3 (and macOS under
   `-sandbox seatbelt-legacy`), TCP to another tenant's loopback ports; on
   macOS, reads outside the deny list (#175).
2. **Tenant → platform** (B1): reach A7, forge A4/props, abuse spawn to
   escalate A6 or exhaust A9, SSRF via egress. → props and the caller are
   unforgeable across nodes; every Worker and facet `fetch()`, redirect,
   WebSocket, `connect()`, Cache API and `node:net` attempt either goes
   through hostd's egress socket to netd's ADR-019 guard or fails (C17,
   `gen.TestEgressSink`); `lim` is enforced at the receiver (C08, RT-01
   fixed); objects are created only under create admission (F4); tenant
   audit is a `tenant_note` only (C09); the mgmt socket needs the host
   token (C10). **Open:** in-process `request.cf` forgery within one
   workload (H08, #148); Node-tier egress is enforced only on Landlock
   ABI ≥ 4 and macOS (not under `-sandbox seatbelt-legacy`).
3. **Identity / crypto** (A4/A6/A3). → the egress MAC is per object with
   a replay cache (C11); the Node tier's `x-tw-caller` carries a nonce;
   custody and issuer keys are 0600 and mode-checked. On-behalf-of
   assertions (RFC §4.6) are **not implemented**.
4. **Kill / revocation** (A9): race the reaper or throttle, survive a
   kill. → `tw kill` tombstones the object and its node answers 410;
   in-flight flows are reset. **Open:** right after a kill, a killed object
   can still answer through its workload for a moment (#94); netmap
   retraction to peers that dial the killed node's IP directly takes
   seconds (RT-07).
5. **Audit** (A8): forge or omit records, tamper with the chain. → chain
   tamper-evidence holds (truncation, gaps, forged signatures caught); host
   records are signed with the key pinned at the host's first
   registration (M4); a flood can't drop host records or break the chain
   (F2); verification reads one consistent snapshot (#130). Tenant notes
   are marked unverified. **Open:** internet- and tenant-supplied strings
   reach the operator's terminal unescaped (H13, #150).
6. **Host / ops.** → a host is trusted only for its leases (B4). The
   operator of a host can read every tenant leased to it, and mgmt's
   operator holds every credential: see "Who you trust" in
   docs/security-model.md. `--policy-apply` merges only additive grants
   within `tag:tw*` (C12). Releases are signature-checked on every install
   and upgrade path (B9). **Open:** placement inputs from a host are not
   cross-checked (H23, #156); warm keys in custody (H25, #158).
7. **Sandboxes.** → as in §3. The Linux escape battery
   (`attacks.TestRT08_LinuxSandboxEscapeBattery`) and the Seatbelt tests
   pin them: `hostd.TestSeatbeltStrictProbes` (real probes under
   `sandbox-exec`), `hostd.TestSeatbeltStrictProfileShape`,
   `hostd.TestMultiTenant` (probes under a running workload's live
   profile), `hostd.TestSeatbeltProfile*` and, in `make test-node`,
   `TestNodeTier/Sandbox` (from inside the Node app). **Open:** the macOS
   profile's read deny list and `localhost:<port>` TCP matching (H26,
   #175); vmhelper input validation (H11, H16).
8. **Internet → workload** (B8): reach an unlisted path, `/_tw` or a DO;
   mint nodes; exhaust a limit from one address; squat a name. → routes,
   `/_tw` and path tricks are refused (C13); `getCaller()` throws on a
   public request (C14); a public request can't mint (I2, F4); per-IP
   connection and request caps (F6); names are owned per tailnet (F7).
   **Open:** the app gets the once-decoded path, so an app that decodes
   again sees `..` (H21, #154).
9. **Tailnet insider / admin's browser** (B6, B7): reach a mgmt route
   without the cap, drive `/ui` or `/v1` from a web page, get destructive
   MCP tools. → WhoIs caps per route; Host allowlist and `browserGuard`
   (H-105 fixed); MCP destructive tools only by an explicit grant.
   **Open:** whether `svc:` grants put app caps in CapMap (H22, #155).

## 5. What is genuinely strong (do not regress)

- netd ingress strips identity headers by prefix and by name; a caller in
  `request.cf` that arrives over the network comes only from WhoIs.
  Within one workload it is not sealed (H08).
- ADR-019 egress guard: netmap-only name/IP resolution; refuses self,
  sibling, host, loopback, link-local, multicast and metadata addresses;
  checked in the handler and the dialer (`netd.TestChain`), and workerd
  has no other way out (C17).
- Per-object egress attribution via unforgeable `ctx.props` and the
  per-object egress MAC; tenant code can't read the MAC key in-isolate.
- Lease epoch fencing (single conditional writes), host-scoped reads,
  and the registration throttle (AIMD).
- Audit hash-chain tamper-evidence, signed host records, and no silent
  drops under flood.
- Cron triggers (`docs/cron-triggers.md`): `scheduled()` runs are reachable
  only via hostd's private instance ingress (`cf.tw.kind = "scheduled"`,
  which only hostd sets). The platform refuses undeclared crons. Runs need
  a fresh heartbeat, so a partitioned host does not run them twice.
  Expressions are bounded and fuzzed (`internal/cron`). Tests:
  `TestCronTriggers/TailnetCallerCannotTriggerScheduled` and
  `TestGeneratedCron/UndeclaredCronRefused`. Overlap on one host past the
  timeout is open (H17, #152).
- Release signatures pinned in every binary and script (B9).
- Secret hygiene: redacting slog handler, pre-commit scan, keys never in a
  guest image.

See [`RED-TEAM.md`](RED-TEAM.md) for the findings by pass and
[`LEDGER.md`](LEDGER.md) for what is open now.
