# tailworks Threat Model (red-team)

Date: 2026-11-08 (first version, red-team Pass 1).
Revised: 2026-11-17 (#118): tiers and sandboxes match the code at master
e62f70a; boundaries B7–B10 added for `/mcp`, public ingress, upgrades and
hosts' push API; attacker goals restated with what is fixed and what is
open.
Revised: 2026-11-17 (follow-up to #121): the macOS process tier matches the
strict Seatbelt profile at master adae38b (#171).
Revised: 2026-11-24 (#333): §3.1 on side channels inside a workload's
process; the §5 MAC-key claim holds against JavaScript only.

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
 tenant code ── globalOutbound (platform Egress + MAC) ── hostd ── netd egress (ADR-019) ─┬─ tailnet (object's node)
                                                                         └─ SSRF guard ── host network ── internet
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
  the receiver. The caller in `request.cf` is sealed by hostd and
  verified before an object sees it, so code in one workload cannot
  forge a caller to a sibling; it can still replay a real caller it was
  sent, and plain DOs share the Worker's isolate (H08, F5). Between
  workloads of one tenant, a service binding call to a named entrypoint
  or RPC method (ADR-053) is admitted from any tailworks node of the
  tenant that the tailnet policy lets reach the target: the grant and the
  node certificate are the check, not the caller's `services`
  declaration. A tenant `fetch()` can't carry the header that marks such
  a call (stripped on egress); a raw socket can (H-binding-wire). All of
  this holds against JavaScript: the objects of one workload share a
  process with the wrapper that holds the MAC root, so they are one
  side-channel domain (§3.1).
- **B4 Host ↔ mgmt.** A host is `tag:tw-host` by WhoIs, bound to its host
  ID by a one-time registration secret (`tw host add`), and trusted only
  for the leases it holds: every host write is fenced by lease holder and
  epoch, and host reads are scoped to its tenants (F1, M1, M3). On the
  local machine, mgmt's unix socket needs the per-host token; same-uid
  peercred alone is not enough (RT-03).
- **B5 Platform ↔ Tailscale control.** One revocable mint credential per
  tenant (OAuth client, or Headscale API key). Object keys are single use,
  pre-authorized, tagged, and expire within 5 minutes; warm keys wait in
  custody for up to 30 minutes. mgmt's own first join key (tag:tw-mgmt,
  single use, pre-authorized, 5-minute expiry) is minted by `tw up` and
  handed to mgmt in `<state>/mgmt/join.env`: 0600, in mgmt's 0700 state
  dir, which the process sandbox denies to workloads. mgmt reads it and
  deletes it before it joins; a failed start deletes it too, and so does
  `tw up` when it returns. A same-uid reader in that window gets one
  short-lived key for a node mgmt is about to take. hostd's join file
  (`<state>/hostd/join.env`) works the same way.
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
- **B11 Workload → internet (egress).** On by default
  (ADR-049); `tailnet.egress.internet` turns it off or
  limits it, and `tw-hostd -worker-internet=off` turns it off per host.
  Flows leave from the host's network, never from a tailnet node, and
  carry no tailnet identity or identity headers. Held by the SSRF guard
  (`netd/publicip`, shared with containers' `enableInternet`): loopback,
  private (RFC 1918, ULA), link-local and metadata, CGNAT and tailnet
  ranges, the host's own addresses and translated forms are refused on the
  address actually dialled, after DNS; any forbidden answer refuses the
  name (no rebinding); special-use names are never looked up. Tailnet
  names never reach host DNS. Since ADR-052 the same path carries raw TCP
  (`connect()`, `node:net`); TCP port 25 is refused by default unless the
  workload's list names the destination with the port
  (`internet_port_refused`, ADR-052 follow-up, #339). **Residual:** a
  Worker can send whatever it can read on the tailnet to the internet; the
  host's resolver sees the internet names it looks up, and tenants on one
  host share its cache (a timing side channel); an upstream sees the
  host's address, so it can't tell tenants on one host apart, and nothing
  caps how many flows a workload opens from it per unit of time
  (docs/limits.md "Limits that don't exist").

## 3. Attacker capabilities per tier

What tenant code can do on the process tier depends on the OS sandbox
`tw-hostd -sandbox auto` resolves to. This table matches
[docs/security-model.md](../docs/security-model.md#what-the-process-tier-sandbox-confines),
which has the full per-platform rows.

| Tier | Native code? | Isolation | Residual attacker power |
|------|-------------|-----------|-------------------------|
| **process, macOS** (Seatbelt) | Node app: yes. workerd: only after a V8 escape | V8 isolate (workerd); Seatbelt `(allow default)` for system services (Mach, sysctls), with reads, writes, network, signals and exec denied and allowed back for what the workload needs (`seatbeltProfile`, `hostd/seatbelt_strict.go`) | Reads only the OS dirs (`/System`, `/usr`, `/bin`, `/sbin`, `/etc`, `/opt`, `/dev`, the time zone database), its runtime, the Node prefix and its own dirs: not `/tmp`, `/var/folders`, `/Users` or `/Library`, and never the home dir, tailworks' state and run dirs or other tenants' credential files. Writes only its own dirs and `/dev/null`, `/dev/zero`, `/dev/dtracehelper`. Network: unix sockets only in its own dirs (not tailworks' sockets or the system tailscaled socket); TCP over IPv4 only, connect only to the ports hostd passes on IPv4 loopback (`tcp4 "localhost:<port>"`: Seatbelt takes no literal address, and 127.0.0.1 is macOS's only IPv4 loopback address; a Node app: its egress proxy and its own port; workerd: none), no TCP bind or listen for a Node app (it listens on a unix socket in its own dir that hostd's ingress dials, so nothing it opens is reachable from the LAN; H-lan-bind), workerd binds only its `tw dev` inspector port; no UDP, so no DNS of its own. Signals only itself and its children (`same-sandbox`): not tw-hostd, tw-mgmt or another workload. Exec only of the runtime, the Node prefix, its own dirs and `/System`, `/bin`, `/opt`, `/sbin`, `/usr`. Node-tier egress is `enforced`. Reads are an allow list and TCP is pinned to IPv4 loopback since [#175](https://github.com/rosskukulinski/tailworks/issues/175) (H26). `-sandbox seatbelt-legacy` (kept for one release) brings back the old files-only profile: TCP anywhere, writes to `/tmp`, signals to any same-user process, unix sockets outside the deny list (including the system tailscaled socket), egress `unenforced`. |
| **process, Linux, Landlock ABI ≥ 4** (kernel 6.7+) | as above | Landlock allow list + seccomp (`internal/procsb`) | Reads only OS dirs (`/usr`, `/lib*`, `/bin`, `/sbin`, `/etc`, `/opt`, `/nix/store`, `/run/current-system`: whole trees, so also every other program's configuration and any world-readable secret there), its runtime and its own dirs. Landlock has no deny rules: hostd's deny list (the home dir, tailworks' state and run dirs, every tenant's credential file) holds only because nothing allows those paths, so a state dir or credential file placed under an OS dir would be readable by every tenant's workload, same uid and all. tailworks refuses that layout at `tw up`, `tw tenant add\|set`, hostd start and in the sandbox launcher (`sandbox_layout_unsafe`, H-342-landlock-deny), and `tw doctor` probes the real paths. Writes only its own dirs; TCP connect only to the ports hostd gives it; a Node app serves only the listener hostd passes it (127.0.0.1) and every `listen()` of its own is a no-op (seccomp; H-lan-bind); no AF_UNIX (seccomp), no UDP/raw; signals only its own process group; no ptrace, mount, bpf, namespaces, io_uring. Residual: the kernel's attack surface, and the same uid as tailworks (an escape from the sandbox is the host operator). |
| **process, Linux, Landlock ABI 1–3** | as above | as above, but Landlock has no TCP rules | As ABI ≥ 4 for files, unix sockets, signals and syscalls. TCP is **unrestricted** (loopback, including other tenants' ports; the LAN; the internet); Node-tier egress `unenforced`. |
| **process, Linux without Landlock** (or `-sandbox none`) | as above | none | Full same-uid access: every tenant's node keys and state, the mgmt token file, every control socket. hostd therefore runs one tenant's process-tier workloads only (`sandbox_unavailable` for a second), unless started with `--insecure-shared-host`. |
| **microVM, Linux** (Firecracker) | yes, inside the guest | a VM per workload; Firecracker under its jailer (chroot, uid, netns, cgroup); per-VM gateway; node keys never in the guest; DO storage on a dm-crypt volume whose key mgmt releases only to the lease holder | Confined to its own workload. Holds its workload's MAC root, so it can forge egress attribution **among its own workload's objects**. After a VMM escape: the jail uid, and the root vmhelper's message surface (the uffd region table is validated, H11; only a VM hostd started as a base can become a snapshot base, H16). Snapshot-restored guests share kernel state from before the reseed (TCP and hash-table secrets); not exploitable across tenants, because guest TCP ends at its own VM's gateway (H24 #157, disclosed; ADR-030). |
| **microVM, macOS** (Virtualization.framework, Apple silicon) | yes, inside the guest | a VM per workload; node keys stay on the host | Confined to its own workload; forges attribution only among its own workload's objects, as above. |

On every tier the workload runs as tailworks' user (or the jail uid) and
the process tier sets no CPU or memory limit (#115). The Node runtime is
process tier only.

**Hardware side channels** (H-host-sidechannel, #334) are a residual on
every tier, because every tier shares the host's cores and kernel with
tw-hostd (which embeds netd), tw-mgmt (assets A1–A3) and the other tenants; the
process or VM boundary is where the OS's and CPU's mitigations apply, and
tailworks turns none of them on. Per tier:

- **process, workerd** (any OS): the attacker runs JavaScript and
  WebAssembly only, with timers and shared-memory threads; Spectre-class
  reads from another process depend on the host kernel's mitigations and
  on SMT. This is the tier Cloudflare's "no native code" argument comes
  closest to, and still not fully: tailworks does not restrict timers or
  `SharedArrayBuffer`.
- **process, Node app** (any OS): native code by design (`node:vm`,
  child processes, native modules, precise timers): `CLFLUSH`, `RDTSC`
  and every published microarchitectural attack are available to it. The
  mitigations and SMT state of the host kernel are the only defence.
- **microVM, Linux**: native code inside the guest, with a VM boundary
  between it and the host; the residual is what KVM and the host kernel
  don't mitigate (L1TF with SMT on, MDS-class sampling, a `Vulnerable`
  entry the microcode leaves), as Firecracker's production host setup
  describes. KSM on the host would also merge pages across guests.
- **macOS, both tiers**: the posture can't be read, so a macOS host is
  development-grade for tenants that don't trust each other.

`tw doctor` reports the Linux posture (`side_channels`) and warns on a
`Vulnerable` entry, SMT on, `mitigations=off` or KSM on; nothing refuses a
second tenant on it (decision in docs/security-model.md, "Side channels").

So: the microVM tier is the boundary that doesn't depend on the host
kernel's same-uid sandbox. On Linux with Landlock ABI ≥ 4 and on macOS the
process tier confines files, sockets and TCP (on macOS, reads only under
a deny list; on Linux, reads under an allow list of OS trees, so
tailworks' own files must stay outside them); on Landlock ABI 1–3 (and macOS under
`-sandbox seatbelt-legacy`) it confines files and unix sockets but not TCP;
without Landlock it confines nothing and the host is single-tenant. hostd's
shared-host checks refuse the unconfined cases (§4.1); a host that confines
files but not TCP is admitted as shared, and `tw status`/`tw doctor` say
so.

### 3.1 Side channels inside a workload's process

Every sandbox in §3 separates processes (or VMs). Inside one workload the
separation is V8's: isolates, which keep JavaScript apart but are not a
boundary against speculative execution (Spectre) or a memory-safety bug
that yields a read without code execution. Cloudflare's production runtime
adds what workerd alone can't: it freezes `Date.now()` while code runs,
offers no shared memory or threads to build a timer from, moves suspicious
Workers to their own process and restarts the runtime so memory moves;
with all of that, researchers still leaked a secret across isolates at up
to 12 bit/s ([Workers security model, 2020](https://blog.cloudflare.com/mitigating-spectre-and-other-security-threats-the-cloudflare-workers-security-model/);
[revisiting Spectre on Workers, 2026](https://blog.cloudflare.com/revisiting-spectre-attacks-on-workers/);
["workerd on its own is not sufficient to protect against hardware bugs
like Spectre"](https://blog.cloudflare.com/workerd-open-source-workers-runtime/)).
tailworks has none of those mitigations, and relies on none of them
between tenants: one workload is one process.

**What sits in a workload's process (process tier).** The platform
wrapper, which holds `MAC_KEY` as a binding of the platform worker
(`hostd/gen/gen.go`, the `MAC_KEY` line) and, under the Linux Landlock
sandbox, `LOCAL_TOKEN` (the token hostd's per-instance loopback listeners
require; `Options.Loopback`); the tenant Worker's isolate, with its plain
DOs; and one isolate per facet object, loaded by the platform. `MAC_KEY` is
the root of the per-object egress MACs (C11), of the caller seal (F5,
H08), of `hostCap` (H27) and of the create ticket (F4). Secrets of other
boundaries are not in the process: node keys (A1), mint credentials (A2)
and custody (A3) are hostd's and mgmt's, in other processes, and hostd
checks every MAC, seal and ticket against the key of the instance whose
socket or listener the request arrived on.

**What a leaked `MAC_KEY` yields**, all of it inside the attacker's own
workload:

1. Egress as a sibling object: a sub-agent whose caps were narrowed by
   `lim` leaves from its parent's node and is admitted with the parent's
   caps (B3, "every object's egress leaves from its own node").
2. A sealed caller of its choice for any object in the workload:
   `getCaller()` names whoever the attacker likes (I3, within the
   workload; a replay of a real caller needs no key, H08).
3. `hostCap`: the facet Host's platform-only entry points (`__tw_kill`,
   `__tw_intercept`, `__tw_alarm_op`; H27).
4. A create ticket, so a public request to the workload admits any object
   name; the budget it spends is the tenant's own (I2, A9).

`LOCAL_TOKEN` yields nothing by itself: using it takes a raw TCP socket to
hostd's loopback listeners, which tenant code in workerd has only after a
V8 escape, and an escape reads the process's memory anyway. So the
boundary this weakens is **B3** (object ↔ object within a workload), not
B1 or B2. Whether the workerd builds tailworks ships have V8's sandbox
enabled is not recorded (the binary carries its symbols; nothing more was
checked), and even with it and per-isolate memory protection keys
Cloudflare says "a single arbitrary read vulnerability within a Worker
process can lead to cross-tenant leakage"
([Safe in the sandbox, 2025](https://blog.cloudflare.com/safe-in-the-sandbox-security-hardening-for-cloudflare-workers)).
Here the tenants of that process are one tenant's objects.

**Measured** (pinned workerd 1.20261121.1, `workerd serve --experimental`,
the generated config, no flag tailworks sets; first measured with
1.20261118.1 in #333): during a CPU-only loop `Date.now()` and
`performance.now()` advance in 1 ms steps (about 200 distinct values each
in 200 ms); `SharedArrayBuffer`, `Atomics.wait` and shared
`WebAssembly.Memory` exist; there is no second thread (`node:worker_threads`'
`Worker` throws `ERR_METHOD_NOT_IMPLEMENTED`), so no counting-thread timer.
Remote timers and long sessions are available by design: internet egress
is on by default (B11), WebSockets and Durable Objects are long-lived, and
there is no CPU or subrequest limit (docs/limits.md, "Limits that don't
exist"). Nothing detects an attack, nothing moves a suspicious object, and
a workload's process restarts only when it is redeployed or crashes. The
measurement is pinned by `gen.TestTenantClocks` (C-clocks), so a workerd
bump that changes the clocks is noticed; the hypothesis itself is
H-sidechannel-inproc, **disclosed**, not demonstrated.

**So:** on the process tier the isolates of one workload and the platform
wrapper are **one side-channel domain**. Object-to-object claims (B3, C05,
C11, H27) hold against JavaScript, not against an object that mounts a
speculative attack. Objects that must not trust each other belong in
separate workloads, which are separate processes. The microVM tier has
the same shape per workload (the guest "holds its workload's MAC root",
§3); what stands between processes or VMs of different workloads on one
host (SMT, kernel mitigations, memory deduplication) is the host's job,
not workerd's: [#334](https://github.com/rosskukulinski/tailworks/issues/334).

**Decisions (2026-11-24, #333; nothing in the code changes, so no ADR):**

- No scheduled restart or rotation of `MAC_KEY`. A restart drops the
  Durable Objects' in-memory state and every WebSocket of the workload,
  and at the published rate a 256-bit key leaves in well under any restart
  interval that would keep the workload usable; it would stop only slow
  leaks. The key is drawn afresh each time hostd starts an instance
  (`gen.Generate` from `startInstance`, `hostd/workload.go`).
- Moving the MAC root out of the process waits on upstream ask WD-3
  (a per-actor `globalOutbound`, RFC §4.8 and §12): hostd would then tell
  objects apart by the connection they arrive on, and no MAC and no key
  would sit in the process.
- Follow-up upstream ask: a frozen-clock switch for self-hosters, so a
  workerd config could opt into the production runtime's clock behaviour.
  Not implemented here. Tenant-selected experimental timer flags are
  [#337](https://github.com/rosskukulinski/tailworks/issues/337).

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
   `-sandbox seatbelt-legacy`), TCP to another tenant's loopback ports.
   On macOS, reads are an allow list since #175.
2. **Tenant → platform** (B1): reach A7, forge A4/props, abuse spawn to
   escalate A6 or exhaust A9, SSRF via egress (the host, its LAN or a
   metadata service, directly or through a rebinding DNS name). → props
   and the caller are
   unforgeable across nodes; every Worker and facet `fetch()`, redirect,
   WebSocket, `connect()`, Cache API and `node:net` attempt either goes
   through hostd's egress socket to netd's ADR-019 guard or fails (C17,
   `gen.TestEgressSink`); internet egress passes the SSRF guard on the
   dialled address (B11, `netd.TestInternetEgress`,
   `hostd.TestWorkerInternetEgress`); `lim` is enforced at the receiver (C08, RT-01
   fixed); objects are created only under create admission (F4); tenant
   audit is a `tenant_note` only (C09); the mgmt socket needs the host
   token (C10); a caller forwarded in-process counts only if hostd's seal
   verifies (H08 fixed, F5). **Open:** Node-tier egress is enforced only on Landlock
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
   docs/security-model.md. The policy write (only after an interactive
   y/N, or `--apply-policy --yes`; #481) merges only additive grants
   within `tag:tw*`, plus two grants tailworks builds itself (the admin
   grant, and `autogroup:member` to a new app's own tag; C12). Releases are signature-checked on every install
   and upgrade path (B9). A host's path reports cover only its own rows
   and its own tenants, are bounded at ingest, and never move a running
   workload by themselves (H23). Warm keys in custody are counted in
   `tw status` and revoked on `tw down` and `tw tenant rm` (H25, #158);
   while mgmt runs, a custody read still yields the live pool.
7. **Sandboxes.** → as in §3. The Linux escape battery
   (`attacks.TestRT08_LinuxSandboxEscapeBattery`) and the Seatbelt tests
   pin them: `hostd.TestSeatbeltStrictProbes` (real probes under
   `sandbox-exec`), `hostd.TestSeatbeltStrictProfileShape`,
   `hostd.TestMultiTenant` (probes under a running workload's live
   profile), `hostd.TestSeatbeltProfile*` and, in `make test-node`,
   `TestNodeTier/Sandbox` (from inside the Node app). The macOS profile's
   reads are an allow list and its TCP is IPv4-loopback only (H26, #175).
   The uffd region table is validated (H11,
   `helper.TestRT3_H11_UffdBadTableFailsSession`), and only a VM started
   as a base can become a snapshot base (H16, `helper.TestRT3_H16_BaseGuard`).
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
  `request.cf` comes only from WhoIs: hostd seals it under the instance
  key and the platform verifies the seal before an object, the Worker or
  `actingFor` uses it (H08).
- ADR-019 egress guard: netmap-only resolution for tailnet names (never
  host DNS); refuses self, sibling, host, loopback, link-local, multicast
  and metadata addresses; checked in the handler and the dialer
  (`netd.TestChain`), and workerd has no other way out (C17). Internet
  egress dials only an address that passed `netd/publicip` after DNS,
  never a name, and never from a tailnet node (`netd.TestInternetEgress`).
- Per-object egress attribution via unforgeable `ctx.props` and the
  per-object egress MAC. JavaScript in a tenant isolate can't read the MAC
  key: it is a binding of the platform worker only
  (`gen.TestGeneratedWrapperProbe/FacetEnvAndPrivacy`). That holds
  against code, not against a speculative or memory-safety read from an
  isolate in the same process (§3.1): within a workload, attribution is
  as strong as the process.
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
