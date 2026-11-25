# Security model

How tailworks decides who is calling, what each workload can reach, and what
stands between tenants. Read this first; the red team's detailed view is
in [THREAT-MODEL.md](../security/THREAT-MODEL.md) (a dated assessment),
[RED-TEAM.md](../security/RED-TEAM.md) (findings and fixes) and
[LEDGER.md](../security/LEDGER.md) (what is still open). To report a
vulnerability, see [SECURITY.md](../SECURITY.md).

## Identity: WireGuard, not tokens

- **Every workload and every Durable Object instance is its own tailnet
  node**, with its own MagicDNS name and the tags from `tailworks.jsonc`. Your
  tailnet policy (grants) decides who may reach which node, as for any
  other device.
- **Callers are identified by the network.** netd asks the tailnet who
  owns the WireGuard peer (WhoIs) and puts the answer in `request.cf`.
  `getCaller(req)` returns that node, its user (for a person's device),
  its tags and the app capabilities its grants carry (`caps()`). Client
  copies of identity headers are stripped on the way in, by prefix
  (`Tailscale-*`, `x-tw-*`, `tw-*`, `X_TW_*`, `tw_*`, `CF-*`,
  `X-Forwarded-*`) and by name (`Forwarded`, `X-Real-IP`, …;
  `isIdentityHeader` in `netd/ingress.go`, [COMPAT.md](COMPAT.md#identity)).
  There are no bearer tokens between workloads. Inside a workload, hostd
  seals the caller (an HMAC under the instance's key, which only hostd
  and the platform wrapper hold), and the platform checks the seal before
  any object, the Worker, or an audit `actingFor` uses it. A caller that
  tenant code attaches to an in-process forward without a valid seal is
  no caller at all. For what a seal doesn't stop, see
  [Known gaps](#known-gaps).
- **tailworks' own API works the same way.** tw-mgmt serves `/v1` on its
  tailnet node and authorizes each request by WhoIs: admins hold
  `github.com/rosskukulinski/tailworks/cap/admin`, approvers `github.com/rosskukulinski/tailworks/cap/approve`, MCP tool users
  `github.com/rosskukulinski/tailworks/cap/mcp`, and a team's own tenants `github.com/rosskukulinski/tailworks/cap/tenant-admin`
  ([agents.md](agents.md), [approvals.md](approvals.md), [teams.md](teams.md)).
  `tw link` stores only mgmt's address; no key or token.
- **One credential mints node keys:** an OAuth client for a Tailscale
  tenant, or the Headscale API key for a Headscale tenant
  ([INSTALL.md](INSTALL.md#the-oauth-client)).
  Each object's key is single use, pre-authorized, tagged and expires
  within 5 minutes (`KeyExpiry` in `mgmt/types.go`). Warm keys, minted
  ahead of time so new objects start faster, wait in mgmt's custody for up
  to 30 minutes ([concepts.md](concepts.md#tuning-how-fast-a-new-objects-own-node-comes-up)).
  `tw status` shows how many each tenant has, when any; `tw down` and `tw tenant rm`
  revoke them. Revoking the credential
  stops all future minting.
- **mgmt's own node key is handed over in a single-use file.** On the
  first start, `tw up` mints mgmt's `tag:tw-mgmt` key (single use,
  pre-authorized, expires within 5 minutes) and writes it to
  `~/.tailworks/mgmt/join.env`, a 0600 file in mgmt's 0700 state
  directory that workloads' sandboxes can't read. mgmt reads the key and
  deletes the file before it joins; a failed start deletes it too, and so
  does `tw up` when it returns. A host's own join key
  (`~/.tailworks/hostd/join.env`) is handled the same way. The join file
  `tw host add` writes for another machine (0600) carries that host's
  single-use key and one-time registration secret, plus non-secret
  settings (host ID, mgmt's URL, control URL); tw-hostd deletes it after
  the join and keeps only the settings and the registration secret beside
  the node state (0600).
- **Kill is a network cut.** `tw kill` tombstones the object, its node
  stops answering (410), and with the Devices scope the device is deleted.

## Isolation tiers

| Tier | Where | What separates a workload from the host and other tenants |
|---|---|---|
| **process** (default) | macOS, Linux | V8 isolates (workerd) or a Node.js process, running as tailworks' own user inside a per-workload OS sandbox (`tw-hostd -sandbox auto`): Seatbelt on macOS, Landlock + seccomp on Linux. What each sandbox confines is below; it differs a lot by platform. |
| **microVM** (`"tier": "microvm"`) | Linux with KVM (Firecracker), macOS on Apple silicon (Virtualization.framework) | A VM per workload. On Linux, Firecracker runs under its jailer (own chroot, uid, network namespace, cgroup) and Durable Object storage sits on a per-workload encrypted volume whose key mgmt releases only to the host that holds the workload's lease. Node keys stay on the host and never enter the guest ([containers.md](containers.md), [INSTALL.md](INSTALL.md#linux-the-microvm-tier-firecracker-kvm)). |

The process tier is not a VM boundary. Don't run code you don't trust on
it.

The process sandbox is on by default. On Linux it needs no root and no
user namespaces, only a kernel with Landlock (5.13+ with Landlock enabled,
for example Ubuntu 22.04+, Debian 12, Fedora): a small launcher in
tw-hostd applies the rules, then execs workerd or node in place. `tw
doctor` checks that the sandbox enforces on your machine (on Linux, the
Landlock ABI too) and, while tailworks runs, that every workload is under
it. Without a sandbox (Linux without Landlock) `tw doctor` warns loudly.

### Side channels inside a workload

On the process tier, one workload is one workerd process. Its Worker,
every one of its objects (each a V8 isolate of its own) and tailworks'
platform wrapper, which holds the per-instance key that signs egress
attribution and callers, all share that process. V8 isolates keep
JavaScript apart; they are not a boundary against CPU side channels such
as Spectre. Cloudflare's production runtime adds mitigations that
open-source workerd does not have: it freezes `Date.now()` while code runs
and offers no shared memory. In the workerd tailworks ships, `Date.now()`
and `performance.now()` advance in 1 ms steps during a CPU loop and
`SharedArrayBuffer` exists (there is no second thread). Cloudflare's own
researchers leaked a secret across isolates at up to 12 bit/s even with
those mitigations in place.

So tailworks' guarantees between the objects of one workload (per-object
egress attribution, `getCaller()` in a sibling object, a sub-agent's `lim`)
hold against JavaScript, not against an object that mounts a
speculative-execution attack on its own process. An object that got the
key could, inside its own workload only, send egress attributed to a
sibling object, make `getCaller()` in a sibling name a caller of its
choosing, and reach the facet Host's platform-only entry points. It gets
nothing of another workload or tenant and none of hostd's node keys: those
are in other processes. (What stands between processes of different
workloads on one host is the host's and the kernel's job; see [When a host
is shared between tenants](#when-a-host-is-shared-between-tenants).) The
rule: **objects that must not trust each other
belong in separate workloads**, which are separate processes; one workload
is one trust domain. The microVM tier is the same per workload (the guest
holds the key). tailworks does not restart workloads on a schedule to
rotate the key: a restart drops Durable Objects' in-memory state and every
WebSocket, and would stop only a slow leak. The clocks tenant code sees are
pinned by a test against the pinned workerd, so a workerd update that
changes them is noticed (ledger `C-clocks`, `H-sidechannel-inproc`).

### What the process-tier sandbox confines

| | macOS (Seatbelt) | Linux, Landlock ABI ≥ 4 (kernel 6.7+) | Linux, Landlock ABI 1–3 |
|---|---|---|---|
| Profile shape | `(allow default)` (Mach services, sysctls) with reads, writes, network, signals and exec denied, then allowed back for what the workload needs (`seatbeltProfile` in `hostd/seatbelt_strict.go`) | allow list (`internal/procsb`) | allow list (`internal/procsb`) |
| Reads | only the OS dirs (`/System`, `/usr`, `/bin`, `/sbin`, `/etc`, `/opt`, `/dev`, the time zone database), the runtime binary (and a Node install prefix) and its own dirs; `/tmp`, `/var/folders`, `/Users`, `/Library` and the rest of the disk are refused. Your home dir, tailworks' state and run dirs, hostd's data and run dirs and every tenant's credential files are refused even where they sit inside an allowed dir. Outside the allow list a workload can only `stat` `/` and the parent dirs of the paths it may read | only the OS dirs (`/usr`, `/lib`, `/bin`, `/sbin`, `/etc`, `/opt`, `/nix/store`, `/run/current-system`), the runtime binary (and a Node install prefix), a few global `/proc` and `/sys` files, the process's own `/proc` entries and its own dirs. Landlock has no deny rules: your home dir, tailworks' state and run dirs and the credential files are unreadable only because nothing allows them, so **none of them may live under one of those OS dirs** (not `--state-dir /opt/tailworks`, not an OAuth client file in `/etc/tailworks/`). `tw up`, `tw tenant add\|set`, `tw-hostd` and the sandbox launcher refuse such a layout (`sandbox_layout_unsafe`), and `tw doctor` probes the real paths. Everything else under `/etc` and `/opt` that your user can read, tenant code can read too | same as ABI ≥ 4 |
| Writes | own dirs only (plus `/dev/null` and similar devices); `/tmp` and the rest of the disk are refused | own dirs only | own dirs only (ABI 1 also refuses renames and links across directories) |
| Unix sockets | only its own (in its own dirs); every other socket, including the system tailscaled socket and DNS (mDNSResponder), is refused | none: seccomp refuses `socket(AF_UNIX)` (except a Node app in `listen: "unix"` mode) | same as ABI ≥ 4 |
| TCP | IPv4 only: connect only to the ports hostd gives it on IPv4 loopback (a Node app: its egress proxy and its own port; workerd: none), so `[::1]:<port>` is refused. Seatbelt can't name a literal address, so the rule is `tcp4 "localhost:<port>"`; `127.0.0.1`, where hostd listens, is the only IPv4 loopback address macOS configures. A Node app may not bind or listen on TCP at all: it listens on a unix socket in its instance dir, which hostd's ingress dials, and hostd forwards `127.0.0.1:<PORT>` to it for self-calls. (Seatbelt's bind rule matches the port, not the address, so an allowed port could be bound on `0.0.0.0` and reached from the LAN.) workerd binds only its inspector port, under `tw dev` | connect only to the ports hostd gives it (a Node app: its egress proxy and its own port; workerd: hostd's loopback services); a Node app binds nothing: hostd passes it its listener (`127.0.0.1:<PORT>`, or its unix socket) as fd 3, and seccomp makes every `listen()` a no-op, so no socket the app creates ever listens, on any address. (Landlock matches port numbers, not addresses, and doesn't see an unbound socket's `listen()`.) | **unrestricted** (Landlock has no TCP rules before ABI 4) |
| Other sockets | none (no UDP, so no DNS of its own; netd resolves names) | TCP only for IPv4/IPv6 (no UDP, ICMP, raw or MPTCP); netlink route only | same as ABI ≥ 4 |
| Signals | only itself and its own children (`same-sandbox`): not tw-hostd, tw-mgmt or another workload | only itself and its own process group | same as ABI ≥ 4 |
| Other syscalls | not filtered; exec only of the runtime binary, its own dirs and the OS dirs (`/bin`, `/usr`, `/opt`, …) | seccomp refuses ptrace, `process_vm_readv/writev`, mount, bpf, perf, keyrings, new namespaces, kexec, modules, io_uring, … | same as ABI ≥ 4 |
| Environment | the list hostd builds for it (`hostd.WorkerdEnv`), not hostd's own: only `TZ`, and only when hostd has one. Nothing of the operator's shell (a foreground `tw up`) or of the service manager (`--service`) reaches workerd, whichever way it is started: `sandbox-exec`, the Landlock launcher or no sandbox. A Node app gets the list in [node-tier.md](node-tier.md). (On the microVM tier, tw-guestd gives its workerd `HOME=/tmp PATH=/usr/bin:/bin TZ=UTC`.) | same | same |
| Node-runtime egress (`tw status`) | `enforced` | `enforced` | `unenforced`: the egress proxy is cooperative |

On every platform the process tier caps each workload's V8 heap and sets
no other CPU or memory limit (see
[Availability between tenants](#availability-between-tenants)), and the
workload runs as the same user as tailworks.

So on Landlock ABI 1–3, a hostile Node app, or workerd code after a V8
escape, can open TCP connections with the host's network identity
(including to other tenants' loopback ports and to services on your LAN).
[node-tier.md](node-tier.md) describes what the Node runtime still guarantees
there. On macOS, `tw-hostd -sandbox seatbelt-legacy` keeps the previous
profile for one release in case the strict one breaks a workload: it
confines reads, writes and unix socket connects under the deny list only,
so TCP, signals to tailworks' own processes and writes elsewhere (`/tmp`)
are open again, and `tw status` reports Node egress as `unenforced`.

### The V8 sandbox in the shipped workerd

The [V8 sandbox](https://v8.dev/blog/sandbox) is a compile-time V8 feature
(`v8_enable_sandbox`) that keeps a memory-corruption bug in V8 from reaching
memory outside the JavaScript heap. **The workerd builds tailworks ships do
not have it**: the npm `workerd` 1.20261121.1 binaries for macOS arm64
(`@cloudflare/workerd-darwin-arm64`), Linux amd64 (`workerd-linux-64`) and
Linux arm64 (`workerd-linux-arm64`), all carrying V8 15.8.62.5. The microVM
guest runs the same Linux binary. Checked 2026-11-24 (issue #338):

- **How it is built.** workerd builds V8 with Bazel, and V8's Bazel build
  on the 15.8 branch hardcodes `sandbox = false` in the build
  configuration it embeds (`bazel/defs.bzl`) and lists `v8_enable_sandbox`
  among the GN flags the Bazel build doesn't support (`BUILD.bazel`).
  workerd's own source says the same (`src/workerd/jsg/jsg.c++`: "We do not
  build workerd with the V8_ENABLED_SANDBOX flag").
- **Measured at run time.** A workerd process serving one Worker reserves a
  single 4 GiB region (V8's pointer-compression cage) and nothing near the
  1 TiB the sandbox reserves at startup. macOS arm64: `vmmap <pid>` of the
  pinned binary. Linux amd64: `/proc/<pid>/maps` of the `workerd-linux-64`
  binary from npm, run on an amd64 VM. Same result on both.
- **Not evidence**, so the next person doesn't repeat the detour: the
  binaries contain the flag names `sandbox_testing` and `sandbox_fuzzing`
  and the text "V8 sandbox violation detected", but V8 defines those flags
  (read-only) in builds without the sandbox too. The string
  `v8_enable_sandbox: 0` in the binary is workerd's copy of Node's
  `process.config` (`src/node/internal/internal_process.ts`), not its own
  build configuration. `process.versions.v8` is empty under workerd, so a
  Worker can't report the V8 version either.

What it means: pointer compression on its own is not a security boundary.
A memory-corruption bug in V8 (a JIT type confusion, say) that hostile
tenant code triggers gives it arbitrary read and write in that workload's
workerd process, which also runs the platform worker for that workload and
its secrets. The next wall is the OS sandbox on the process tier (the table
above: Seatbelt, or Landlock and seccomp) and the VM on the microVM tier,
which is why the pinned workerd's age matters (`tw doctor`'s `workerd_age`)
and why the nightly `v8-check` job watches Chrome's stable releases for the
V8 fixes ([RELEASING.md](RELEASING.md), "Upstream advisories").

To recheck when the pin moves: `make v8-check` prints the V8 version; run
the new workerd on a one-Worker config and look at its address space
(`vmmap <pid>` on macOS, `/proc/<pid>/maps` on Linux). A 4 GiB reservation
and nothing larger means no sandbox; a reservation of hundreds of GiB or
more means it is on, and this section should change.

### When a host is shared between tenants

tailworks runs a second tenant's process-tier workloads on a host only when
the host passes these checks, unless you start it with
`tw up --insecure-shared-host` (tenants are then not isolated from each
other). The flag is saved in tw.json and kept by every later `tw up`, so
`tw up` and `tw status` print it on a `! unsafe` line each time
(`unsafeFlags` in `--json`), with `--allow-experimental-flags` and
`--local-dev`. The checks:

- **No sandbox** (Linux without Landlock, or `-sandbox none`): the second
  tenant is refused (`sandbox_unavailable`), and so is a Node app on a host
  that already serves another tenant (`egress_unenforced`).
- **Your user can reach a container engine** (or is in the
  docker/podman/lxd/incus groups): refused.
- **Your user can reach the system tailscaled socket**: refused on Linux
  when a Node app uses `listen: "unix"`. (On macOS the Seatbelt profile
  refuses every unix socket outside the workload's own dirs; under
  `-sandbox seatbelt-legacy` it refuses only those under the deny list,
  and hostd doesn't check for the tailscaled socket.)

`tw doctor` reports the last two as `host_sockets`: it warns if *your
user* can reach the system tailscaled's socket or a container engine, or
is in the docker/podman/lxd groups. To share a host, run tailworks as a
dedicated user without them.

A host whose sandbox works but doesn't confine TCP (Landlock ABI 1–3, or
macOS with `-sandbox seatbelt-legacy`) **is admitted** as a shared host.
`tw status` reports its Node workloads as `egress unenforced` and
`tw doctor` warns. If you share such
a host between tenants that don't trust each other, use
`"tier": "microvm"`, a Linux host with kernel 6.7+, or macOS with the
default Seatbelt profile.

#### Side channels

The sandboxes above control what a workload may read, write and reach.
They do nothing about what it can *infer* from the hardware it shares
with its neighbours: cache timing, speculative execution (Spectre and its
successors), a sibling hyper-thread's execution ports, or pages the kernel
merged between processes. tailworks relies on two things here: a process
(or a VM) per workload, so the OS's and CPU's mitigations apply at the
process or VM boundary, and **the host kernel's and CPU's mitigations
being on**. Nothing in tailworks turns them on. On a shared host they are
what keeps one tenant's code from reading another tenant's secrets, or
tw-hostd's and tw-mgmt's (every tenant's node keys, the custody key, the
mint credential) off a shared core. There is no complete defence against
Spectre-class attacks for any isolation technology; the mitigations make
them impractical, not impossible.

"No native code" does not hold on two tiers, so don't count on it there.
The Node runtime runs native code by design (`node:vm`, child processes,
native modules, with precise timers and threads), and a microVM guest runs
whatever the tenant ships. workerd on the process tier runs only
JavaScript and WebAssembly, which is a weaker attacker but still one with
timers and shared-memory threads.

What the operator keeps on, on a Linux host that serves more than one
tenant, runs a Node-runtime workload or offers the microVM tier:

- **Kernel mitigations on**: the kernel's defaults, no `mitigations=off`;
  every entry in `/sys/devices/system/cpu/vulnerabilities/` reads
  `Mitigation:` or `Not affected`, none `Vulnerable`. Keep the CPU
  microcode current.
- **SMT (hyper-threading) off** (`nosmt`, or `echo off >
  /sys/devices/system/cpu/smt/control`), or accept that a tenant's sibling
  thread can watch another tenant's cache and port use. Firecracker's
  production guidance says off.
- **KSM (kernel samepage merging) off** (`/sys/kernel/mm/ksm/run` is `0`).

`tw doctor` reads these (`side_channels`, Linux only) and warns on a
`Vulnerable` entry, SMT on, `mitigations=off` or KSM on, saying when it
matters. A macOS host can't be read this way (and Seatbelt's process tier
shares the kernel and the cores with every tenant), so a macOS host is
**development-grade** for tenants that don't trust each other.

*Decision (issue #334):* this is a warning, not a gate. hostd does not
refuse a second tenant on a host that reports `Vulnerable` or
`mitigations=off`, as it does for a missing sandbox. A single-tenant host
is the common case and would gain nothing; the kernel's `Vulnerable`
strings vary by CPU and kernel (`Vulnerable: Safe RET, no microcode` is a
partial mitigation), so a gate would need its own judgement of each; and an
operator switch to override it would be needed anyway, as
`--insecure-shared-host` is today. Read the warning before you add a
tenant.

### Experimental compatibility flags

Every workerd tailworks starts runs with `--experimental`, because the
platform worker needs it (the worker loader, facets). Without that switch
workerd refuses the compatibility flags upstream marks experimental
(`unsafe_module`, which gives `workerd:unsafe` and string eval; `rtti_api`;
`precise_timers`; and whatever a workerd release adds). So that the switch
doesn't also unlock them for tenant code, a deploy whose
`compatibility_flags` list one is refused (`experimental_flag`): by mgmt at
the deploy and its dry run, from the known list (`internal/compatflags`),
and by the host when it generates the workerd config, from the list and
from the pinned workerd itself (a 20 ms `workerd test` of a throwaway
config without `--experimental`). `tw up --allow-experimental-flags` (or
`tw-hostd -allow-experimental-flags`) lets a host run them; it advertises
the `experimental-flags` feature and mgmt places such deployments only on
hosts that have it. Everything an experimental flag unlocks stays inside
the tenant's own workerd; the policy is about not handing out APIs upstream
calls not ready (issue #337, [COMPAT.md](COMPAT.md#experimental-flags)).

### Availability between tenants

The sandboxes above are about confidentiality and integrity: what a
workload can read, write and reach. They don't give tenants their own
share of the machine. What each tier does about one tenant using up a
host:

| | process tier | microVM tier |
|---|---|---|
| Memory | each V8 isolate's heap is capped (`tw-hostd -workerd-heap-mib`, default 1024 MiB; a Node app's `--max-old-space-size`, default 2048 MiB). Past it, that workload's workerd or Node process aborts and hostd restarts it. Memory outside the V8 heap (native buffers, WebAssembly memory) is not capped | the guest's memory (`-vm-mem-mib`, default 512 MiB) bounds everything in the workload |
| CPU | not limited: a busy workload competes with every other process on the host | the guest's vCPUs (`-vm-vcpus`, default 2) |
| Disk, logs, telemetry | bounded per workload ([limits.md](limits.md)) | as on the process tier, plus the guest's volume (`-vm-volume-mib`) |
| Requests | not limited per workload on the tailnet: your tailnet policy decides who may call it. Public (Funnel) traffic is rate-limited per workload and per client IP | the same |

So **availability isolation between tenants is not provided on the
process tier**: the heap cap stops one runaway allocation from taking the
host's memory, but a tenant can still slow its neighbours down with CPU,
native memory or a flood of requests from callers your policy lets in.
If tenants that don't trust each other share a host and that matters,
put them on the microVM tier or on separate hosts.

mgmt protects itself from its tailnet callers: each WhoIs node has its
own request-rate bucket (hosts a larger one), and non-host callers share a
bounded number of in-flight requests, so neither a script nor a flood
from one admin's machine can starve the hosts' heartbeats. Over either
limit is `429 rate_limited` ([limits.md](limits.md#management-api)).

## Egress

- **Tailnet egress** leaves from the workload's node, or from the object's
  own node in facet mode ([egress-modes.md](egress-modes.md)), so your
  tailnet policy decides what each object can reach, and the other side's
  `getCaller` sees which object called. Tailnet names resolve from the
  node's netmap only, never from host DNS; a tailnet name that isn't in the
  netmap is refused (`peer_not_visible`).
- **Internet egress is on by default** (ADR-049). A
  `fetch()` to a public host leaves from the **host's own network**, never
  from a tailnet node: the destination sees the host's source address
  (different on each host) and no tailnet identity, and tailworks strips
  its identity headers. `tailnet.egress.internet: false` turns it off and a
  list limits it ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet));
  `tw up --worker-internet=off` (`tw-hostd -worker-internet=off` on a host
  added with `tw host add`) turns it off for a whole host. A refused
  destination is `internet_egress_unavailable`.
- **What a workload can do with the host's address.** Since ADR-052,
  `connect()` (and `node:net`, and the Node runtime's CONNECT) reaches any
  public address on any TCP port, from the host's own address, and the
  destination can't tell tenants on one host apart: whatever a workload
  sends, the operator's address takes the blame for. The controls are
  `tailnet.egress.internet: false` or a list, per workload, and
  `-worker-internet=off` per host. Two things are bounded by default:
  **TCP port 25 is refused** (`internet_port_refused`, audited as `deny`),
  so a prompt-injected agent can't relay mail as the operator unless the
  workload lists the relay with the port (`["smtp.example.com:25"]`;
  submission ports 465 and 587 stay open); and an object has at most 256
  open sockets. **Not bounded:** how many flows or `fetch()` calls a
  workload starts per unit of time
  ([limits.md](limits.md#limits-that-dont-exist)). A workload that is
  allowed internet egress can scan, stuff credentials or flood from the
  host's address until it is killed; the audit trail (every TCP flow,
  the first `fetch()` per destination, every refusal) records it
  afterwards.
- **The SSRF guard always applies.** netd refuses loopback, private
  (RFC 1918, ULA), link-local (including `169.254.169.254` and other cloud
  metadata), CGNAT `100.64.0.0/10` and the tailnet ranges, the host's own
  addresses, and translated forms of these (IPv4-mapped, NAT64, 6to4,
  Teredo), with `forbidden_destination`. It checks the address it actually
  dials, after DNS: a name with **any** forbidden answer is refused (no DNS
  rebinding), and names under special-use suffixes (`.internal`, `.local`,
  `.localhost`, `.lan`, …) are refused without being looked up.
- **What host DNS sees.** An internet name that the workload's setting
  allows is resolved by the host's resolver, which sees the name (not which
  tenant asked). Tenants on one host share that resolver's cache. A
  workload that must not leak its lookups turns internet egress off and
  uses a gateway on the tailnet ([recipes.md](recipes.md#call-an-internet-api)).
- **Through an exit node.** A host started with
  `--worker-internet=exit-node=<node>` sends every workload's internet
  flow through that tailnet exit node, via the host's own node
  (`tag:tw-host`), so the destination sees the exit node's address and the
  host's resolver never sees the names (they resolve at the exit node, or
  at a `dns=` resolver through it). The guard above is unchanged: netd
  still resolves, checks every answer and dials the checked address; the
  exit node forwards packets and resolves nothing. When the exit node is
  unusable the host refuses with `exit_node_unavailable` rather than use
  its own network. A workload can require this with
  `tailnet.egress.via: "exit-node"`
  ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressvia)). The exit
  node's operator sees every flow's destination and bytes (TLS stays end to
  end from netd), attributed to the host, never to a tenant or object.
- **Audit.** The first internet flow per object and destination is an
  `egress` audit record naming the path and the address dialled; refusals
  are `deny` records.
- **What this means for your data.** A Worker that can read something on
  the tailnet can now send it to the internet. Give a workload that holds
  sensitive tailnet data `"internet": false` unless it needs the internet.
- This is netd's guard on the path every `fetch()` takes; code that
  escapes workerd's V8 sandbox, and a Node app, are held to it only where
  the OS sandbox confines TCP (see the table above). Containers' internet
  egress uses the same guard and the same setting
  ([containers.md](containers.md)).

## Trust boundaries

| Boundary | Who is trusted | What holds it |
|---|---|---|
| Tenant code ↔ tailworks | tailworks, never the tenant's code | the tier's sandbox or VM; the platform wrapper sets `request.cf`; egress goes through netd's guard |
| Tenant ↔ tenant on one host | neither | per-tenant netd registries (one tailnet's netmap each), per-workload sandboxes and 0700 state directories; one tenant only without a sandbox |
| Object ↔ object in a workload | the workload's code | per-object nodes and egress attribution; a sub-agent's capabilities are narrowed to what its spawner allowed (`lim`) |
| Host ↔ mgmt | mgmt | WhoIs `tag:tw-host`; every host write is fenced by its lease epoch |
| Admin's browser ↔ mgmt | mgmt's own pages only | every tailnet request must name mgmt in `Host` (its MagicDNS names or an IP literal), which defeats DNS rebinding; browser mutations need a same-origin `Origin`/`Sec-Fetch-Site` ([ADMIN-UI.md](ADMIN-UI.md#browser-origin-guard-csrf-dns-rebinding)) |
| Agents ↔ mgmt (`/mcp`) | mgmt | the same WhoIs gate as `/v1`; read tools need `…/cap/mcp` or the admin cap, and deploy, kill and undeploy need a grant that names them ([agents.md](agents.md)) |
| tailworks ↔ Tailscale control | the tailnet admin | one revocable mint credential; tags you own in `tagOwners` |
| Release ↔ host | the release signing keys pinned in each binary and script | `get.sh`, `install.sh`, `tw upgrade` and hostd's opt-in self-upgrade verify the release's signed `SHA256SUMS`; tw-mgmt can order an upgrade but can't sign one ([upgrading.md](upgrading.md)) |
| Exit node ↔ host | the exit node's operator (only with `--worker-internet=exit-node=…`) | sees each internet flow's destination and bytes, attributed to the host's node, never to a tenant or object; TLS is end to end from netd ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressvia)) |
| Internet ↔ workload | nobody | tailnet-only by default; public routes are opt-in paths over Funnel on `tag:tw-public`, and `getCaller` throws on a public request ([public-ingress.md](public-ingress.md)) |

## Secrets and state

- Worker secrets are sealed in mgmt's custody (AES-256-GCM per entry) and
  reach only the workload's own process; mgmt's database never holds a
  value ([secrets.md](secrets.md)).
- The state directory (`~/.tailworks`) holds node keys and the custody key.
  Back it up like a secret ([INSTALL.md](INSTALL.md#back-up-and-restore)).
  On Linux, keep it, the run dir and every credential file outside the
  OS dirs the process sandbox lets workloads read (`/usr`, `/lib*`, `/bin`,
  `/sbin`, `/etc`, `/opt`, `/nix/store`, `/run/current-system`); tailworks
  refuses to start otherwise (`sandbox_layout_unsafe`).
- The audit trail is hash-chained, so truncation and gaps show
  (`tw audit`).

## Who you trust

| Who | What they can see and do |
|---|---|
| **Host operator** (root, or anyone who can act as the user tailworks runs as on a host) | Read and impersonate every tenant with workloads leased on that host, across tailnets. Each object's node state (keys) lives in 0700 directories that tw-hostd owns and uses; Worker secrets are plaintext in the 0700 instance directory ([secrets.md](secrets.md)); process-tier Durable Object storage is a plain directory; a microVM's encrypted volume is opened with dm-crypt on the host. Lease fencing and volume encryption protect a tenant from *other* hosts and from a copied disk image, not from the host that holds the lease. |
| **mgmt operator** (whoever runs tw-mgmt or can read its state directory) | Holds the custody key (sealed Worker secrets, microVM volume keys), each tenant's mint credential and warm keys, the database and the audit trail. Can mint node keys with a tenant's tags, read every secret, and choose which host runs which workload. |
| **Tailnet admin** | Owns the tags and grants. Decides who can reach which workload and object and which capabilities they carry, and can revoke the mint credential or remove devices. Sees every tailworks node as a device. |

On a single-machine install all three are usually you. When you run
tailworks for other people, they trust the operators of mgmt and of every
host their workloads are placed on.

The CLI also runs some of your project's own code on the machine you
deploy from. When the wrangler config has a custom build
(`"build": {"command": "…"}`), `tw deploy`, `tw policy` and `tw dev` run
that command in a shell before bundling, as wrangler does, and print it
(`› build      <command>`); `--wrangler` runs the project's own wrangler,
which runs it too. This is the same trust as `npm run build` in that
directory: deploy only projects whose code you would run yourself
([wrangler.md](wrangler.md#custom-builds)).

## Telemetry and the update check

tailworks sends no telemetry, usage data or crash reports anywhere. (`tw
telemetry` configures OTLP export from your workloads to a collector you
run on your tailnet; nothing goes to the tailworks project.)

The only request the CLI makes without being asked is the update check:

- **What:** `GET https://api.github.com/repos/rosskukulinski/tailworks/releases/latest`
  with a `tw/<version>` User-Agent. Nothing else is sent: no identifiers,
  no tailnet or workload names. GitHub sees your IP address and the version.
- **When:** after a successful interactive command, at most once every 24
  hours. The answer is cached in `<state>/update-check.json`
  (`~/.tailworks` by default); when a newer release is out, `tw` prints a
  one-line notice on stderr.
- **Never:** after `tw init` or `tw dev` (both run with no tailnet, no
  credentials and no outbound request of their own; under `tw dev`, your
  Worker's internet `fetch()` calls do go out, as on a host), `tw version`, `tw completion` or
  `tw mcp`; with `--json`; when stderr isn't a terminal; with `CI` set; or
  in a development build.
- **Off:** `TW_NO_UPDATE_CHECK=1`. `tw version --json` reports whether it
  is on (`updateCheck`).

Commands you run about releases (`tw version --check`, `tw upgrade`) also
contact GitHub, to look up and download the release.

## Known gaps

The red team's open items are tracked in
[LEDGER.md](../security/LEDGER.md), each with a GitHub issue (label
`security`). Among them:

- **A sealed caller can be replayed inside its workload.** Tenant code
  that received a request from Alice can forward it, or copy its
  `request.cf`, to another object in the same workload for five minutes
  (the seal's lifetime), and that object's `getCaller()` says Alice. It
  can't make a caller nobody sent: forging or swapping one yields no
  caller (ledger H08, F5). Three more limits apply. A `cf` passed as
  plain data, rather than on a `Request`, isn't checked. A `Request` passed
  as an RPC argument is checked for facet objects but not for plain DOs
  (`"egress": "workload"`, and internal classes: `"node": false` or not
  in `tailnet.objects`). And plain DOs run in the same isolate as the
  Worker, so code that rewrites that isolate's JavaScript built-ins can
  defeat any check made there. Facet objects (classes listed in
  `tailnet.objects` with tags, in the default egress mode) each get
  their own isolate.
- **Isolates are not a side-channel boundary inside a workload.** The
  objects of one workload and the platform wrapper that holds its key
  share one process, and the workerd tailworks ships does not freeze
  clocks. An object that mounted a speculative-execution attack on its
  own process could forge egress attribution and callers among its
  siblings, never beyond its workload. Put objects that must not trust
  each other in separate workloads ([Side channels inside a
  workload](#side-channels-inside-a-workload); ledger
  `H-sidechannel-inproc`, disclosed).
- **A named entrypoint is not a boundary inside a tenant.** A
  [service binding](wrangler.md#service-bindings) call to a named
  entrypoint or an RPC method is authorized by the tailnet grant and by
  the caller being a tailworks node of the same tenant. The caller's
  `services` declaration is not checked by the target. tailworks removes
  the header that marks such a call from a tenant's `fetch()`, so a
  request that injected input caused can't forge one. Code that opens a
  raw socket to the target (`connect()`, or a Node app) writes the header
  itself, and the target honours it. Grant a workload's tags only to the
  workloads that may call it, and check `getCaller()` in an entrypoint
  that needs more ([ADR-053](design/adr/053-service-bindings.md), ledger
  H-binding-wire).
- **Snapshot-restored microVMs share some kernel secrets.** On Linux,
  microVMs restored from one base snapshot (rebuilt at least hourly) get
  fresh randomness before tenant code runs. They still share the kernel
  secrets created before the snapshot: the TCP sequence-number and port
  keys, the hash-table seeds, and the kernel layout (on amd64 the layout
  is the same for every guest anyway: Firecracker guests there have no
  KASLR slide). A
  tenant can't use these against another tenant. A guest's TCP never
  leaves its host: it ends at that VM's own gateway, and guests have no
  network path to each other. Linux can't re-key these secrets after boot
  ([ADR-030](design/adr/030-microvm-snapshot-restore.md), ledger H24).
- **On-behalf-of assertions** (`actingFor`) aren't implemented.
