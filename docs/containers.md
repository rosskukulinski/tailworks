# Containers and code sandboxes: `ctx.container` on the microVM tier

tailworks runs Cloudflare's container surface: `ctx.container` in a Durable
Object, the `Container` class from `@cloudflare/containers`, and the Sandbox
SDK (0.x and 1.0) on top of it. Each container is its own microVM attached to
one object. The decision is
[ADR-032](design/adr/032-containers-code-sandboxes.md). This page explains
how to use it first, then how it works.

## Using containers

### Declare the class

Declare containers the way wrangler does: a `containers` entry per DO class,
and the class bound as a Durable Object.

```jsonc
// wrangler.jsonc
{
  "containers": [
    { "class_name": "Sandbox", "image": "./Dockerfile", "instance_type": "lite", "max_instances": 4 }
  ],
  "durable_objects": { "bindings": [{ "name": "Sandbox", "class_name": "Sandbox" }] }
}
```

- `image` is a Dockerfile (or build directory), or a registry ref
  (`docker.io/...`). `tw deploy` builds Dockerfiles on your machine with
  `docker buildx`, pushes them to tailworks' registry, and pins every image by
  digest (see [Images](#images-build-registry-pinning-host-pull-adr-032-d3)).
  The host never builds and never runs a container engine.
  `image_build_context` sets the build context (default: the Dockerfile's
  directory) and `image_vars` the build args (`--build-arg`), as on
  Cloudflare; a named image takes `build_context` and `build_vars`. Build
  vars are not secrets: they end up in the image and in the bundle's
  wrangler config.
- Other keys of a `containers` entry (`rollout_*`, `constraints`, …) are
  ignored, with one warning line each. `name` and `scheduling_policy`
  `"default"` or `"durable_object"` (one container per Durable Object, what
  tailworks does) get none.
- `instance_type` is one of Cloudflare's types or custom resources (below).
  `max_instances` caps the running containers of that class for the tenant.
- tailworks.jsonc can add `"containers"` too: the same array form, or
  `{"<Class>": {"image": ref, "images": {name: ref}}}` to pin refs (Sandbox SDK
  1.0 named images). A pin may only name that class's wrangler images.
- A bundle with containers is placed only on a host that offers
  `containers`, for the image's arch, with a free VM in its capacity
  vector (`503 no_host`; with `--host` or a sticky lease holder that lacks
  `containers`, `409 tier_unavailable`, and `tw deploy --replace` moves it
  instead). This applies even when the parent object itself is process-tier.
  Smart placement (ADR-033: the best measured tailnet path to the
  workload's `tailnet.connects`) only ranks the hosts that pass these
  filters; the current holder is kept while it still offers `containers`
  (capacity aside: it already holds its share).

### Use it

The app code is Cloudflare's, unchanged. These run unmodified on tailworks (the v1
acceptance tests):

- **Sandbox SDK 0.x** (`@cloudflare/sandbox` 0.12.x, `examples/minimal`):
  `getSandbox(env.Sandbox, id)`, `sandbox.exec('python3 -c "print(2 + 2)"')`,
  `writeFile` / `readFile`. Its Bun server runs in the image
  (`docker.io/cloudflare/sandbox:0.12.14`) and is reached via `getTcpPort`.
- **Sandbox SDK 1.0** and raw `ctx.container`:
  `ctx.container.start({image, entrypoint: ["sleep", "infinity"], enableInternet: false})`,
  `ctx.container.exec(argv).output()`, and 1.0's `Files` helper, which runs the
  image's `sandbox-shim` through `exec`.

The `ctx.container` methods tailworks implements:

| Method | On tailworks |
|---|---|
| `start({env, entrypoint, enableInternet, image, instance, labels})` | ✅ returns at once (no readiness wait); throws if already running. `image` must be one of the class's pinned images |
| `running`, `inspect()` | ✅ |
| `exec(argv, {stdin, stdout, stderr, cwd, env, user, signal, pty})` | ✅ streamed stdio, `kill()`, `exitCode`, `output()`; `pty` + `resize()`. No shell, no timeout, env not inherited except `PATH` (Cloudflare semantics) |
| `getTcpPort(p).fetch()` | ✅ HTTP to a port the container listens on (on `0.0.0.0`) |
| `getTcpPort(p).connect()`, WebSocket upgrades to a container port | ❌ `not_supported` (deferred) |
| `monitor()`, `signal(n)`, `destroy(reason?)` | ✅ |
| `setInactivityTimeout(ms)` | ✅ ≤ 6 h; default 10 min |
| `interceptOutboundHttp(addr, fetcher)`, `interceptAllOutboundHttp(fetcher)`, `interceptOutboundHttps(addr, fetcher)` | ✅ enforced (see [Egress](#egress-what-a-container-can-reach)) |
| `start({containerSnapshot})`, `snapshotContainer()`, `directorySnapshots` | ❌ `not_supported` (v1.1) |
| Sandbox SDK `exposePort()` / preview URLs (`proxyToSandbox`) | ❌ `exposePort()` throws `not_supported`; a preview-proxy request gets `501 not_supported` (needs public ingress, ADR-029) |

### Instance types

Cloudflare's types (checked 2026-11-13), or custom
`{vcpu, memoryMib, diskMb}` up to 4 vCPU, 12 GiB and 20 GB:

| Type | vCPU | Memory | Disk |
|---|---|---|---|
| `lite` | 1/16 | 256 MiB | 2 GB |
| `basic` | 1/4 | 1 GiB | 4 GB |
| `standard-1` | 1/2 | 4 GiB | 8 GB |
| `standard-2` | 1 | 6 GiB | 12 GB |
| `standard-3` | 2 | 8 GiB | 16 GB |
| `standard-4` | 4 | 12 GiB | 20 GB |

Each container is a VM with the type's whole vCPUs rounded up (at least 1)
and its memory as guest RAM. A fractional vCPU is a CPU quota: `cpu.max` on
the VMM's cgroup on Firecracker, `cpu.max` on the guest's container cgroup on
vz. The disk is a fresh sparse scratch disk (the overlay's writable layer),
gone when the VM stops. `pids.max` applies in the guest (and to the VMM on
Firecracker). Container processes hold at most Docker's default capability
set minus `CAP_MKNOD`, so root in an image can't lift these limits.

Admission at `start()`, in order: the host's capacity vector (VMs, memory,
vCPU with overcommit, scratch disk; shared with microVM DOs), else
`503 capacity_exhausted`; then a synchronous reservation in mgmt against the
tenant's quotas (`maxContainers` 10, `maxContainerMemMiB` 16384,
`maxContainerVCPU` 8 by default; `tw tenant set --max-containers ...`) and
the class's `max_instances`, else `429 container_quota_exceeded`. If mgmt is
unreachable the start fails closed (`503 quota_unavailable`), except a
restart of a container this host already holds a reservation for. Containers
are not nodes: they don't count against `maxObjects`.

### Egress: what a container can reach

A container has **no tailnet node and no tailnet reach**. Its network ends at
a per-container proxy on the host:

- **By default, nothing.** DNS answers only names the container may reach;
  every other name is NXDOMAIN, every other TCP connection is reset, other
  UDP is dropped. That includes the tailnet, the parent's node, the host,
  link-local/metadata addresses and private ranges.
- **Interceptors are the way to the tailnet.** `interceptOutboundHttp("api.internal", handler)`
  delivers the container's matching requests to the **parent object's**
  handler. The parent decides what to relay, and its own `fetch()` leaves
  with the parent's identity, egress policy and caps. Treat the request as
  untrusted input. `interceptOutboundHttps` works the same over TLS: each
  container gets its own ephemeral CA, installed in the image at
  `/etc/cloudflare/certs/cloudflare-containers-ca.crt` and in its trust
  bundles. Up to 128 interceptors per container.
- **`enableInternet: true`** reaches only the hosts on the workload's
  `tailnet.egress.internet` allowlist in tailworks.jsonc (or any public
  host when it is `true`; none when it is absent or `false`,
  [tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet)), never a private, tailnet,
  metadata or host address (even through a DNS name that resolves to one).
  Each new destination is audited as `egress` with `actingFor: "sandbox-of
  <object>"`, and refusals as `deny`. The traffic leaves from the host's
  own network (`tw-hostd -container-internet=direct`, the default), or
  through the host's tailnet exit node (`exit-node`, with
  `-worker-internet=exit-node=<node>`;
  [tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressvia)); `off`
  refuses it.
- **Inbound** is only the parent's `getTcpPort(p).fetch()`.

### Lifetime

- `start()` boots the VM; the container runs until its entrypoint exits,
  `destroy()` (SIGTERM, then SIGKILL after at most 10 s, hostd's
  `ContainerGrace`), its inactivity timeout, or the host's hard max
  lifetime (24 h by default, hostd's `ContainerMaxLife`).
- When the entrypoint exits the VM is killed, and `monitor()` resolves on
  exit code 0 and rejects with `Container exited with unexpected exit
  code: N` otherwise.
- The inactivity timeout counts from the last container API call with no
  exec, port or stdin stream open. A parked `monitor()` is not activity.
- It belongs to the object, not to the isolate: it survives the object's
  eviction and a blue/green redeploy of the parent (the new code sees
  `running`). It dies with its host; nothing migrates.
- `tw kill <object>`, stopping the workload, and fencing (ADR-028; a
  container inherits its parent class's fence) all SIGKILL the VM at once.
- Disks are ephemeral. There are no snapshots yet.

### Platforms

| Host | Containers | Notes |
|---|---|---|
| Linux arm64, KVM (Firecracker) | ✅ verified (Lima, nested KVM) | the stock `cloudflare/sandbox` images are amd64 only, so tailworks swaps in its arm64 rebuilds through the alias map; app code is unchanged. **Prerequisite:** the rebuilds are not published yet, so the operator builds them once (`guest/images/build-sandbox-images.sh`, Docker) and pushes each with `tw image push` (see "Public bases" below); until then an arm64 deploy of `cloudflare/sandbox` fails with `image_unavailable`. Once an alias has a published `source`, mgmt fetches it on first use instead |
| Linux amd64, KVM (Firecracker) | ⚠ built, not verified | stock images run as published (no alias). The amd64 microVM tier passed `make e2e-vm` on a nested KVM test host (2026-11-22), but `make e2e-container` has not run on amd64 yet |
| macOS, Apple silicon (vz) | ✅ verified | needs `mkfs.erofs` (`brew install erofs-utils`). With `tw-hostd -vm-rosetta` the stock amd64 images run under Rosetta; without it, the arm64 rebuilds |
| no KVM / NAS / process-only hosts | ❌ | they don't offer `containers`; a container bundle is not placed there and `start()` throws `tier_unavailable` (no fallback) |

`tw dev` uses the process tier, so `ctx.container.start()` throws
`tier_unavailable` there.

### Not supported (v1)

- Preview URLs (`exposePort`, `proxyToSandbox`): need public ingress (ADR-029).
- `getTcpPort().connect()`, raw TCP and WebSocket upgrades to container ports.
- `snapshotContainer`, `containerSnapshot`, directory snapshots, backups,
  bucket mounts, persistent sandbox volumes.
- Memory snapshots and fork (also: container VMs never come from the
  microVM warm pool or a base snapshot; they cold-boot).
- HTTP/2 and WebSocket upgrades through interceptors (HTTP/1.1 only).
- A container with its own tailnet node (`sandbox.tailnet.tags`), and
  standalone containers not attached to an object.
- Image signing (cosign) and registry garbage collection.
- A tenant-owned gateway for `enableInternet` (the host's network, or the
  host's exit node, is used).
- Nested virtualization and GPUs (non-goals).
- Workers bindings the Sandbox SDK templates may also use (KV, R2, Workers AI)
  remain unsupported on tailworks as before (docs/COMPAT.md).

## How it works

```
DO (workerd, any tier) ── ctx.container (polyfill in the generated wrapper)
   └─ platform TailnetApi (attests the object) ── HOSTD binding ── hostd /v1/container…
        └─ container manager, keyed (tenant, workload, class, object)
             └─ hostd/vm BootContainer ── tw-vmhelper ── jailer ── Firecracker   (Linux)
                                       └─ tw-vz (Virtualization.framework)       (macOS)
                  guest: tw-guestd (PID 1) ── overlay(erofs image + scratch) ── ENTRYPOINT/CMD, execs
```

- **One VM per container, one container per object.** On Firecracker its own
  jail uid, network namespace and cgroup (`cpu.max` carries fractional
  vCPUs, `memory.max`, `pids.max`); the parent's DO volume, volume key,
  egress MAC key and tailnet identity are never part of it.
- **Image:** an OCI image becomes a read-only erofs (`hostd/image`; per-tenant
  content-addressed cache, `public` scope for operator-marked bases). Each run
  gets a fresh sparse ext4 scratch: the overlay's upper layer, gone with the
  VM.
- **Guest:** the same guest rootfs and tw-guestd as DO VMs, in container
  mode (`guest.json` "container"); the kernel is `guest/build-kernel.sh`'s
  (erofs) on Linux and `guest/build-kernel-vz.sh`'s on macOS. Every
  container process starts through `tw-guestd ctr-exec`: a re-exec in a new
  mount namespace that `pivot_root`s into the overlay and detaches the old
  root, so no container process sees guestd or its config. It installs a
  seccomp filter (`socket`/`socketpair(AF_VSOCK)` and `io_uring_setup` fail
  with `EPERM`; vsock is guestd's channel to the host, and io_uring could
  make sockets out of seccomp's sight; nothing else is filtered), drops
  every capability outside Docker's default set minus `CAP_MKNOD` (no
  `CAP_SYS_ADMIN`, so no cgroup2 mount; no `CAP_SYS_PTRACE`, so no
  `/proc/1/root`), then sets the image's user.
- **No device nodes.** A guest has no device cgroup (Docker's reason for
  keeping `CAP_MKNOD`), so container root can't make nodes at all, and every
  container mount is `nodev`: the overlay root, its lower and upper, and
  `/dev`, a tmpfs whose `null`, `zero`, `full`, `random`, `urandom` and
  `tty` are bind mounts of the guest's own. Nothing in the container opens
  the VM's disks (config drive, rootfs, image, scratch), including a node an
  image ships. `mknod` of a FIFO still works; `mknod` of a device is `EPERM`.
- **Both backends share the host side** (`hostd/vm/container.go`: exec
  streams, events, ports) and the same NIC: a userspace stack with no gateway
  services whose only DNS server and TCP destinations are the run's sandbox
  egress proxy (`ContainerSpec.Net`).
- **vz differences** (macOS has no jailer, cgroups or `mkfs.ext4`): the
  scratch disk is a sparse file the guest formats
  (`ContainerConfig.ScratchFormat`); fractional vCPUs are `cpu.max` on the
  guest's container cgroup (`ContainerConfig.CPUQuotaUS`); `pids.max` is the
  guest's only. Isolation is the hypervisor plus one VMM process per
  container.
- **Rosetta** (`-vm-rosetta`): guestd registers Rosetta with binfmt_misc
  using the `F` flag, so it works in every container mount namespace. The
  backend reports `amd64` as an emulated arch; hostd then accepts
  linux/amd64 images from `-container-image-dir` and drops its local alias
  map. Images pinned by mgmt follow the host's registered arch (arm64), so
  through the registry a Rosetta Mac still runs the arm64 rebuilds.

## Host ⇄ guest protocol (guest/proto)

- Control: the existing vsock NDJSON channel (guest → host port 1024). New
  ops `exec`, `signal`, `resize`, `status`; events `ctr-ready`, `ctr-exit`,
  `exec-exit`, `log`. A connection opens with a `hello` line carrying the
  boot's control token (`GuestConfig.CtlToken`, 256 random bits, on the
  config drive only); the host closes any connection without it, so nothing
  but guestd can open, replace or forge the channel. guestd reconnects
  whenever its connection dies (it always has; a vsock transport reset
  around a VMM snapshot would cause one), and an authenticated reconnect
  replaces the channel (DO guests too).
- Warm-pool and snapshot VMs (fast-vm; Durable Object guests only, see
  [Warm pool and snapshots](#warm-pool-and-snapshots)): a pooled VM boots
  with a pre-claim token on its pool config drive, and every copy restored
  from one base snapshot holds that base's token in guestd's memory, so the
  pre-claim token is *not* unique per VM. It authenticates only the
  connection before the claim, when the VM holds no tenant code (just
  tailworks' warm-up app, which has run and exited) and no keys. The claim's
  `load` op carries a fresh 256-bit `Load.CtlToken`, together with the
  entropy, boot id and clock: hostd switches to it before it sends the load
  (from then on the pre-claim or base token is refused), and guestd adopts
  it before it answers and uses it for every later reconnect. So each
  claimed VM has its own token, like a cold-booted one. Exec stream tokens
  are per exec and only exist in container guests, which are never pooled.
- Exec stdio: one more guest-initiated vsock connection per exec, whose first
  line is `{"type":"stream","exec":<id>,"token":<t>}`, `t` being the random
  per-exec token the host sent in the `exec` op; the host attaches it to the
  exec only with that token (constant-time compare, once); then frames (1 byte kind, u32 BE
  length, payload): stdin / stdin-EOF (half-close) host → guest, stdout,
  stderr, exit (int32) guest → host. No helper change was needed: the
  helper's single vsock listener carries both kinds.

## getTcpPort: host stack dial, not vsock forwarding

`getTcpPort(p).fetch()` dials the guest's TCP port `p` through the VM's
existing userspace network stack (gVisor, `vnet.dialGuest`), exactly how
hostd reaches a DO guest's workerd. It needs no guest-side forwarder and no
new listener on the host. Nothing else can reach a container port: the
container has no tailnet node and no address outside its own VM's stack,
so only its parent object (through hostd, attested) gets in. A
server must listen on `0.0.0.0` (as on Cloudflare). Not listening →
Cloudflare's error string `the container is not listening`, which
`@cloudflare/containers` relies on.

## The sandbox egress proxy (ADR-032 D4)

A container guest's NIC ends at a per-run proxy in hostd's process
(`netd/sbxnet`), not at any tsnet server: it has no tailnet identity, no
egress MAC key, and none of the gateway services a DO guest gets (attributed
egress, workload egress, host API). The guest's `resolv.conf` names the
gateway; that is the only DNS server it has.

- **Deny-all by default.** DNS answers only names the container may reach;
  each gets a synthetic address from `198.18.0.0/15`, so the proxy knows the
  name of every flow without trusting a `Host` header or SNI. Every other
  name is NXDOMAIN and every other TCP flow is reset before its handshake
  completes. UDP (other than DNS), ICMP and IPv6 aren't served.
- **`interceptOutboundHttp(addr, fetcher)` / `interceptAllOutboundHttp` /
  `interceptOutboundHttps`.** `addr` is a host, `host:port`, an IP,
  `ip:port`, or `*.suffix` (no port: any port); at most **128** per
  container (Cloudflare's limit; re-registering a key replaces it). A
  matching request is delivered to the **parent object**: hostd sends it
  through the workload's current instance to the platform wrapper
  (`cf.tw.kind = "intercept"`, which only hostd can set), which calls the
  object's `__tw_intercept`; the polyfill runs it through the handler
  registered under that key. So whatever the handler fetches leaves with the
  object's own identity, egress policy and `lim` (facet mode: the object's
  node; plain-DO mode: the workload node). The request URL is what the
  container connected to; its `x-tw-*`/`tw-*`/`cf-*` headers are dropped;
  the rest is untrusted input. Handlers live in the object's memory: one
  registered before the object restarted answers `502
  intercept_unavailable` until it is registered again
  (`@cloudflare/containers` re-registers on wake).
- **HTTPS interception** terminates TLS with a **per-container ephemeral
  CA** (ECDSA P-256, made at `start()`, valid for the run plus margin).
  Its key stays in hostd's memory and is dropped with the run; only the
  certificate enters the guest, at
  `/etc/cloudflare/certs/cloudflare-containers-ca.crt` (Cloudflare's path)
  and appended to the image's trust bundles where they exist
  (`/etc/ssl/certs/ca-certificates.crt`, `/etc/pki/tls/certs/ca-bundle.crt`,
  `/etc/ssl/cert.pem`, resolved inside the container root). Another
  container's CA does not verify it, and a client that doesn't trust it sees
  the interception. `interceptAllOutboundHttp` is HTTP only; TLS to a host
  with no HTTPS intercept is never terminated.
- **`enableInternet: true`** reaches only hosts on the workload's
  `tailnet.egress.internet` allowlist (names, `*.suffix`, IPs or CIDRs,
  each with an optional `:port`; `true` allows any public host), spliced
  as raw TCP (TLS stays end to end). TCP port 25 is refused unless an
  entry names it (`"smtp.example.com:25"`), and an entry that names a port
  covers that port only ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet)). The name is resolved on the
  host and refused if **any** address is forbidden (ADR-019: loopback,
  private and link-local/metadata ranges, `100.64/10` and the tailnet IPv6
  range, NAT64/6to4/Teredo, the synthetic pool, the host's own addresses).
  Every first flow per destination is audited `egress` with `actingFor:
  "sandbox-of <object>"`; refusals are audited `deny` (the first 32 per run)
  and listed in `tw-hostd`'s `ContainerStatus.Refused`. The path is the
  host's own network (`tw-hostd -container-internet=direct`, the default),
  the host's tailnet exit node (`exit-node`) or none (`off`).
- **Preview URLs** (`exposePort`, `proxyToSandbox`) need public ingress
  (ADR-029): on a container class `exposePort()` throws `not_supported`,
  and a Sandbox SDK preview-proxy request (`x-sandbox-preview-proxy`) gets
  `501 not_supported`.

## No keys in a sandbox

`tw host vmcheck <workload> --container <Class>/<name>` scans the running
container guest's disks (config drive, image, scratch when readable), its
files and a full RAM dump for: `tskey-`, every node/machine private key on
the host, the parent's egress MAC keys (instance root and the object's
derived key), every volume key (DEK) of the tenant's workloads on the host,
the host's own key/token/credential files, and the run's HTTPS-intercept CA
private key. Each needle kind must exist on the host (positive control),
and a per-run canary held in guestd's memory must be found in the dump.
On vz, Virtualization.framework refuses to save the VM's state (as for DO
guests), so the disks and guest files are scanned but RAM is not; the
report's control then reads `FAILED: guest RAM not scanned` and the command
exits `keys_in_guest`.

## Images: build, registry, pinning, host pull (ADR-032 D3)

```
developer machine            tw-mgmt (tsnet, WhoIs)                       host (tw-hostd)
tw deploy                    /v2/  OCI registry                            no container engine
  docker buildx (per arch) ─▶   <tenant>/<workload>-<class>  ◀─ pull by digest ─ image.Store.GetPinned
  alias contexts              public/…  (operator bases)                      verify, flatten, mkfs.erofs
  push over your tailnet  ─▶  POST /v1/deployments {containerImages}          cache: <tenant>/ or public/
                              pin every ref → per-arch digest (mirror external refs)
```

- **Build (`tw deploy`, `internal/imagebuild`).** A class whose wrangler
  `image` is a Dockerfile (or build dir), or whose `images.<name>` has a
  `dockerfile`, is built **on the developer's machine** with `docker buildx`,
  once per target arch (`--platform`, default: the arches of the live hosts in
  the tenant's pool that offer containers), into an OCI layout, and pushed to
  `tw.local/<tenantID>/<workload>-<class>[-<name>]` over the CLI's own mgmt
  connection (a linked developer: their own Tailscale connection; WhoIs). The
  result (`…@sha256:…`, an index for several arches) goes to mgmt in
  `DeployRequest.containerImages`; it must be in the deploying tenant's
  namespace. The app's Dockerfile is used as written: the **alias map**
  (`GET /v1/images/aliases`; default `cloudflare/sandbox:0.12.14/1.0.0/1.0.1`
  → tailworks' arm64 rebuilds on arm64) becomes BuildKit named contexts
  (`--build-context docker.io/cloudflare/sandbox:0.12.14=oci-layout://…`) for
  every form the Dockerfile writes the ref in (FROM, `COPY --from=`, an ARG
  default), fetched from tailworks' registry (`public/`), else the alias's
  digest-pinned `source` (`docker-image://ghcr.io/…@sha256:…`, which BuildKit
  pulls itself), else from the local Docker store. (A `docker-image://`
  context naming a `tw.local` ref makes BuildKit try the network first: 100 s
  of timeouts, measured.)
- **Registry (`internal/registry`, served by tw-mgmt at `/v2/`).** The OCI
  distribution push/pull subset on mgmt's own listeners (tsnet `:80`/`:443`,
  and the local-dev socket), behind the `/v1` WhoIs gate: admins push and pull
  every namespace; hosts only pull, and only `public/…` and the namespaces of
  tenants they hold a deployment of; a host never pushes. One namespace per
  tenant (`<tenantID>/…`; no tenant may be called `public`). Blobs are
  content-addressed files under `<state-dir>/registry/blobs` (`-registry-dir`;
  replicas serving the registry must share it), stored once, but **visible
  only through a repository they were pushed or mirrored into**: the store's
  `registry_links` (SQLite or Postgres, metadata only) say which. Another
  namespace's `HEAD`/`GET` of the same digest is `404 BLOB_UNKNOWN`, a
  cross-namespace mount is refused (a fresh upload instead), and a manifest may
  only reference content already in its own repository. Manifest pushes are
  audited (`image_push`). Routes and roles are documented in
  `api/openapi.yaml` (tag `registry`, excluded from generation).
- **Pinning (deploy).** mgmt resolves every image reference of every class to
  a **linux/arch image manifest digest, once**, for the arches of the pool's
  container hosts, and records the pins in the deployment
  (`ContainerClass.pins`, in `Assignment.containers` and
  `GET /v1/deployments/{id}`); placement is then limited to arches every image
  resolved for (an amd64-only image in a mixed pool lands on an amd64 host).
  `tw.local/<tenant>/…` refs resolve in the tenant's namespace,
  `tw.local/<other>` only ever in `public/`. An **external ref**
  (`docker.io/…`) is mirrored now into `<tenant>/mirror/<registry>/<repo>`
  through **mgmt's own egress** (anonymous bearer tokens for Docker Hub;
  `-no-mirror` refuses external refs; `-registry-insecure` lists plain-HTTP
  upstreams for tests), unless it is a public base. The alias map applies per
  arch. Retagging the upstream after the deploy changes nothing that runs; the
  next deploy picks the new digest. Audited (`image_pin`, with every pin).
  `tw deploy` prints a `✓ image` line per pin of a ref it did not build
  (`<Class> <ref> linux/<arch> → <repository>@<digest> (<from>)`) once the
  deployment is ready, and lists every pin in `--json`'s `images`.
- **Public bases.** `tw image public add <ref> [--arch …]` mirrors an
  upstream image into `public/<registry>/<repo>` (or names a `tw.local` ref
  already in `public/`) and marks its per-arch digests public; deploys of every
  tenant then pin those, so hosts cache it once. Unmarking (`rm`) leaves
  pinned deployments as they are. The **alias targets** (tailworks' arm64
  rebuilds, `tw.local/cloudflare-sandbox:<v>-arm64`) live in `public/` too. An
  alias may carry a `source`: the rebuild as published
  (`ghcr.io/rosskukulinski/cloudflare-sandbox:<v>-arm64@sha256:…`, by
  `.github/workflows/publish-sandbox-images.yml`). When a deploy pins an alias
  target that isn't in `public/` yet, mgmt copies the `source` there through
  its own egress, verified against the digest, tagged as the target, once for
  every tenant; the pin's `from` still reads `alias <target>`, so `image_pin`
  audits show the substitution. **Until the rebuilds are published, the default
  aliases have no `source`** and the operator fills the targets by hand: build
  them with `guest/images/build-sandbox-images.sh OUT_DIR` (Docker), then
  `tw image push OUT_DIR/cloudflare-sandbox-<v>-arm64.tar
  tw.local/cloudflare-sandbox:<v>-arm64`. With `-no-mirror` mgmt never fetches
  a `source`, so the push is always how the targets get there; the
  `image_unavailable` message says so. `tw-mgmt -image-aliases <file>` replaces
  the map (`[{from, to, arch, source}]`; a `source` must be pinned by digest).
- **Host pull (`hostd/image`).** hostd starts a container only from the pin
  for its own arch, pulled as a host from tailworks' registry over the API's own
  connection (`mgmtapi.Client.OCI`: never Docker Hub, no container engine),
  every manifest and blob verified against its digest as it arrives. The cache
  scope comes from the pin's repository, not from the tenant: `public/` is
  shared, the tenant's own namespace is its alone, any other namespace is
  refused before anything is fetched; so a cache hit never tells one tenant
  which images another runs. Pinned entries are keyed by manifest digest.
  `-container-image-cache-max-mib` caps the converted cache (default 20480,
  `tw up` too; 0 = no cap): least recently used first; images of running
  containers, and the one a start just converted, are held, so the cache can
  sit above the cap while they run. Converting one image
  is capped too (`image.Limits`): `-container-image-max-mib` uncompressed
  layer bytes (default 40960, twice Cloudflare's 20 GB disk; counted while
  decompressing and from the entries' sizes, so a decompression bomb stops
  at the cap), 128 layers, 1 Mi entries, path depth 256 and length 4096; an
  image over one fails with `image.ErrLimit` and leaves nothing behind. `-container-image-dir`
  (a directory of tarballs) remains for tests and hosts without a registry.
- **Signing** (cosign) is deferred (ADR-032).

## How `running` is kept

`running` is synchronous in Cloudflare's API: the polyfill keeps it in the
object's SQLite kv, reconciles it with hostd in `blockConcurrencyWhile` at
construction, updates it from every answer, and keeps a background
`monitor` long-poll (re-armed by each call) to flip it when hostd stops the
container.

## Attribution (who may drive a container)

Every op is scoped to the attested caller's own container key; exec IDs
only work for the container that made them; an object never sees another's
container (not even its existence). Facet mode: the object is the
TailnetApi props, unforgeable. Plain-DO mode: the tenant service claims the
object (as for `ctx.tailnet`, docs/egress-modes.md): code in the workload
could drive another plain object's container of the same workload (same
trust domain), never a facet object's and never another workload's.

## Warm pool and snapshots

The fast-vm work (master, ADR-030) gives Durable Object microVMs a warm pool
of pre-booted, tenant-free VMs (`tw-hostd -vm-pool`, on by default) and
restores from one shared base snapshot per host for pool misses
(`-vm-snapshot`, on by default, rebuilt hourly). **Container guests use
neither: every container cold-boots** (`BootContainer`, ~250 ms to the
entrypoint on Firecracker in Lima). Reasons:

- A container VM is shaped by its start(): instance type (memory, vCPUs,
  CPU quota), pids limit, its own image drive and a fresh scratch drive, its
  sandbox NIC (no gateway services, its egress proxy), and the container
  kernel. A pool would need one per shape, image and kernel.
- ADR-032 D6's rule that bases be tenant-free means a base can't hold a
  tenant's image; and an image drive or scratch disk swapped in after a
  restore (the way the DO volume slot works) is not built.
- Every restore must reseed entropy and reset the clock; the load op does
  that for workerd guests, and container guests have no load op.

The pool and the snapshot code share the boot path with containers
(`launch`: config drive, jail, helper, vnet, boot stage timestamps). A
container VM counts against the pool's huge-page budget like any VM of the
backend (so a busy container host refills its pool less), its stage
timestamps are recorded the same way, and the uffd helper and the
`attach`/`snapshot-base`/`Restore` helper ops are never used for it. vz has
no pool or snapshots (every VM there cold-boots). Whether containers should
get a pool is an open decision in STATUS.md.

<a id="host-tiers"></a>
## Hosts that offer containers

A host offers `containers` (and mgmt places container bundles
on it) only when containers can actually run there: a microVM backend whose
helper (Firecracker) or kernel (vz) supports container guests, a guest
kernel with erofs, and `mkfs.erofs`.

`tw doctor`'s `containers` row says whether this host offers them, and
if not, why (the reason tw-hostd found when it started; never a failure).
`tw up`'s tier line ends `, containers` when it does, and `; containers
off: see tw doctor` when the microVM tier is on but containers are not.
When `tw deploy` finds no live host offering containers to build for, its
`no_host` fix lists the live hosts and their tiers; `--platform` builds
anyway. `tw deploy` checks `docker buildx version` before it builds.
