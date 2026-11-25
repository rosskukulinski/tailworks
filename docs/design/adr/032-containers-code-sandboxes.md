# ADR-032: Containers and code sandboxes: Cloudflare's `ctx.container` on the microVM tier, one VM per container, attached to its DO, no tailnet reach by default

**Date:** 2026-11-13
**Status:** Accepted (Ross, 2026-11-13: "Build it all"). Implementation: tailworks branch `slimy-pig` (in progress).

**Context.**
- Agents increasingly need to **run arbitrary code**: generated scripts,
  tool binaries, `git`, Python and headless browsers.
  - tailworks marks `harness/container/*` ❌ (tailworks `docs/COMPAT.md`).
  - Cloudflare offers Containers and the Sandbox SDK.
  - tailworks' microVM tier (ADR-007, ADR-024; tailworks M8, L3) already runs
    jailed Firecracker VMs with no tailnet keys in the guest.
- **Cloudflare's surface moved** (verified 2026-11-13 against
  developers.cloudflare.com and the Apache-2.0 sources):
  - **`ctx.container` is now the primitive.**
    - `@cloudflare/containers` (the `Container` class, `getContainer`,
      `getRandom`) is **deprecated**, replaced by `ctx.container` used
      directly in a plain DO (containers/guides/migrate-to-durable-object-container-api).
    - The API: `start({env, entrypoint, enableInternet, image, instance,
      containerSnapshot, labels})`, `running`, `inspect()`,
      `exec(argv, {stdin, stdout, stderr, cwd, env, user, signal, pty})`
      returning an `ExecProcess`, `getTcpPort(p).fetch/connect`,
      `monitor()`, `signal()`, `destroy()`, `setInactivityTimeout(ms)`
      (≤ 6 h), `interceptOutboundHttp(s)` and `snapshotContainer()`
      (rootfs only) (containers/api/durable-object-container).
    - **Internet is off unless `enableInternet` is set.**
    - Inbound traffic arrives only via the DO.
    - Disks are ephemeral.
    - Shutdown is SIGTERM, then up to 15 min, then SIGKILL.
    - Instances of the new `durable_object` scheduling policy never roll.
    - Images are **linux/amd64** only.
  - **Sandbox SDK 0.x (0.12.14, maintenance only)** gives `getSandbox`,
    `exec`, `writeFile`/`readFile`, processes, sessions, the code
    interpreter, `exposePort` and backups.
    - It is a `Container` subclass that talks HTTP/SSE (or capnweb RPC)
      to a Bun server on port 3000 inside `cloudflare/sandbox:0.12.14`.
  - **Sandbox SDK 1.0** has no in-container server. Its `Files` helper
    runs a static `sandbox-shim` through `ctx.container.exec()`.
  - **Both generations rest on `ctx.container`.** Both `cloudflare/sandbox`
    tags are published for amd64 only (Docker Hub, checked).
  - **workerd:** `ctx.container` is backed by the capnp `rpc::Container`
    interface (`io/container.capnp`). The only engine is
    `containerEngine.localDocker`, "only used for local development"
    (`workerd.capnp:1029`).
    - Its client also needs a `proxy-everything` sidecar,
      `/networks/bridge`, and ingress on workerd's own `127.0.0.1`
      (`container-client.c++:1197`).
    - Emulating the Docker API is therefore brittle.
- tailworks' sandbox seam (`hostd/sandbox.Backend`/`Spec`) assumes a workerd
  guest.
- Placement capacity is `CapacityNodes − LiveNodes` only.
- hostd's `tierFor` silently downgrades microvm to the process tier.

**Decision.**
1. **API surface: implement `ctx.container` (D1).**
   - **How:** the generated platform wrapper, which already subclasses
     every DO class, hands the class a `ctx` whose `.container` is a tailworks
     polyfill. The polyfill calls hostd over the existing `hostd` outbound
     service, which drives the VM. No workerd patch.
   - **Result:** the legacy `Container` class, Sandbox 0.x and Sandbox 1.0
     run unmodified on top.
   - **Upstream ask (WD-n):** a pluggable `containerEngine` (an external
     `rpc::Container` server), which retires the polyfill as WD-3 retires
     the egress wrapper.
   - There is no tw-native `env.SANDBOX` in v1. Any later sugar wraps
     `ctx.container` and adds no second backend.
   - **v1 methods:** `start`, `running`, `inspect`, `monitor`, `signal`,
     `destroy`, `setInactivityTimeout`, `exec` (piped stdio, streaming,
     `kill`; `pty`/`resize` if cheap), `getTcpPort().fetch` and
     `interceptOutboundHttp`/`interceptAllOutboundHttp`/
     `interceptOutboundHttps`.
   - **Acceptance:** both of these run unmodified:
     - the **0.x minimal example** (`getSandbox` + `exec` +
       `writeFile`/`readFile`);
     - the **1.0 flow** (raw `ctx.container.exec` + `Files` through
       `sandbox-shim`).
2. **Unit and lifecycle (D2).**
   - **One Firecracker VM per container, one container per DO
     instance,** separate from the parent's process or VM and never
     shared.
   - **Attached:** the DO is the only controller.
   - hostd owns the VM, keyed by `(tenant, workload, class, object)`, not
     by the isolate.
     - It **survives DO eviction** until its inactivity timeout.
     - It **survives a parent blue/green redeploy:** the new code sees
       `running` and decides, as in Cloudflare's `durable_object` policy.
     - It **dies with its host**; nothing migrates.
   - **ADR-028:** a container **inherits its parent class's fence bit.**
     The risk is two live copies under one identity, not disk state.
   - `tw kill <object>` destroys the container.
3. **Images (D3).**
   - **Format:**
     - The OCI image is flattened on the host into a read-only **erofs**,
       addressed by digest.
     - Each instance gets an ephemeral sparse ext4 **scratch disk**
       (overlay upper).
     - tailworks' kernel and `tw-guestd` stay PID 1 and run
       `ENTRYPOINT`/`CMD` as a child.
   - **Build:** `tw deploy` builds from the `Dockerfile` **on the
     developer's machine**. It pushes to a **mgmt-hosted OCI registry on
     tsnet** (WhoIs-authenticated, one repo per tenant). External
     `image:` refs are mirrored into it at deploy.
   - **Hosts pull only from the tailworks registry and never run a container
     engine,** so the shared-host socket rule is unchanged.
   - **Pinning:** by digest, resolved once at deploy. Signing (cosign) is
     deferred.
   - **Host cache:** content-addressed, **per tenant by default**. An
     operator can mark images "public base" (e.g. `cloudflare/sandbox`) to
     share them across tenants. Otherwise cache hits would reveal which
     tenant uses which image.
   - **Architecture:**
     - Hosts register `arch`, and the image must match.
     - **amd64 microVM assets (kernel, guestd) are a v1 prerequisite,** so
       stock images run as-is on x86.
     - On arm64, tailworks publishes arm64 rebuilds of `cloudflare/sandbox` (the
       Bun server and `sandbox-shim` are Apache-2.0) and swaps them in
       through a documented alias map; the app code doesn't change.
     - On macOS, see (7) for Rosetta.
4. **Networking and identity (D4).** A sandbox has **no tailnet node and
   no direct tailnet reach by default.**
   - **NIC:** its TAP sits in its own netns and goes to a host-side
     sandbox egress proxy in netd, not to any tsnet server.
   - **Nothing to protect in the guest:** no keys, no egress MAC key, no
     identity material. It cannot forge attribution.
   - **Egress:**
     - **Deny-all by default.** DNS answers only for allowed or
       intercepted hosts. The ADR-019 blocks always apply.
     - **`interceptOutboundHttp(s)` is the tailnet route.** Requests are
       delivered to the parent DO's handler, which decides what to relay
       under its own identity, policy and `lim`. This is Cloudflare's
       credential-proxy pattern.
     - **`enableInternet: true`** goes through the tenant's exit or
       gateway path. It is capped by the class's `egress.internet`
       allowlist and audited as `sandbox-of <object>`.
   - **Ingress:** only the parent's `getTcpPort(p).fetch()`.
   - **Preview URLs** (`exposePort`, `proxyToSandbox`) are deferred to the
     public-ingress design (ADR-029). In v1 they throw `not_supported`.
   - **Later, opt-in:** `sandbox.tailnet.tags` gives the sandbox its own
     node with a tag that the default policy grants nothing.
5. **Untrusted-code posture (D5).**
   - **Threat model:** prompt injection becomes `exec`, so the sandbox is
     hostile from its first instruction.
   - **It is a separate, lower trust domain than its parent.**
     - It has its own VM, jail uid, cgroup and netns.
     - The parent's DO volume, DEK and MAC key never enter it.
     - Its exec output and intercepted requests are untrusted input to the
       parent.
   - **Limits:** from Cloudflare instance types (`lite`…`standard-4`, or
     custom `{vcpu, memoryMib, diskMb}`), enforced host-side:
     - vCPU count plus `cpu.max` for fractional types;
     - guest memory;
     - scratch-disk size;
     - `pids.max` in the guest and on the VMM;
     - inactivity timeout ≤ 6 h, plus an operator **hard max lifetime**
       (default 24 h).
   - **Kill:** `destroy`, `tw kill` and fencing all end in a VMM SIGKILL
     plus jail teardown (sub-second; M8 measured 14 ms). The `destroy()`
     SIGTERM grace is capped.
   - **No-keys scan:** `vmcheck --keyscan` runs on sandboxes in e2e with a
     stricter needle set. It adds the parent's MAC key and DEK, the
     tenant's volume keys and registry credentials, all of which must be
     absent.
   - **Non-goals:** nested virtualization (no `/dev/kvm` in the guest) and
     GPUs. Headless Chromium on CPU is in scope.
   - **No fallback.** hostd **refuses** any microVM assignment it can't
     run (`tier_unavailable`, reported to mgmt) instead of downgrading.
     This applies to the whole microVM tier (it replaces `tierFor`'s
     warning path). `ctx.container.start()` without a backend throws.
6. **Storage (D6).**
   - **Ephemeral by default.**
   - **v1.1:** `snapshotContainer` with Cloudflare semantics (rootfs
     only). The overlay upper is captured as a layer in the tenant's
     registry repo, and `start({containerSnapshot})` boots base + layer.
     Retention follows Cloudflare (30 days, 20 GB), with operator
     overrides.
   - **Deferred:**
     - an encrypted persistent sandbox volume (ADR-024 machinery, but its
       own DEK, never the parent's);
     - bucket mounts and `createBackup`/`DirectoryBackup`, which assume R2
       and presigned URLs.
   - **Memory snapshots (fork/resume) are deferred to the fast-vm line,
     with three rules fixed now:**
     1. Warm-pool and golden bases are tenant-free (consistent with
        ADR-030, snapshot restore).
     2. A snapshot of a running sandbox restores only within its tenant,
        and only for the same object unless explicitly forked.
     3. Every restore reseeds entropy and resets the clock before tenant
        code runs.
7. **Platforms (D7).**
   - **Linux/KVM, amd64 and arm64:** Firecracker.
   - **macOS: a Virtualization.framework (vz) backend in v1,** as a
     second `sandbox.Backend`.
     - **The full microVM tier,** for DOs and containers, both in
       `tw dev` and on real Mac hosts.
     - It uses `VZVirtioSocketDevice` for the vsock protocol and a
       file-handle NIC for the host-side network (tailvisor's macOS
       pattern).
     - It needs the `com.apple.security.virtualization` entitlement, so
       release signing changes.
     - It has no jailer equivalent: isolation is the hypervisor plus one
       process per VM.
     - Rosetta for Linux is expected to run stock amd64 images on Apple
       Silicon (**to verify**).
   - **No Docker path anywhere.**
   - **NAS and no-KVM hosts don't offer containers** (refused per 5).
8. **Build vs adopt (D8).**
   - **Keep driving Firecracker directly** by extending `hostd/vm`, the
     helper and the jailer.
   - **`tw-guestd` gains a container mode:**
     - erofs + overlay pivot;
     - the image config (env, cwd, user, entrypoint);
     - exec, files and signals over vsock NDJSON, plus a stdio byte-stream
       channel.
   - **Rejected:**
     - firecracker-containerd and Kata: both put containerd on the host
       (the root-equivalent socket class), and Kata's agent duplicates
       guestd.
     - gVisor: deferred, though it is a possible no-KVM tier.
   - **cloud-hypervisor** is the escape hatch behind `sandbox.Backend` if
     GPU, virtiofs or hotplug become goals.
9. **Scheduling and quota (D9).**
   - **Always co-located with the parent DO;** there is no runtime
     placement.
     - A bundle declaring containers requires, at deploy, a host offering
       `containers` for the image's `arch` (the `offersTier` path:
       `503 no_host` / `409 tier_unavailable`).
     - That holds even when the parent is process-tier.
   - **Host capacity is a vector:** `{nodes, vms, vmMemMiB (huge and 4K),
     vcpus (with an overcommit ratio), scratchDiskMiB}`.
     - A `start()` over capacity gets `503 capacity_exhausted`; there is
       no spill.
     - Deploy reserves nothing, because containers start lazily.
   - **Quota:** sandboxes are not nodes, so they don't count against
     `maxObjects` or `CapacityNodes`.
     - New tenant quotas: `maxContainers` (running), `maxContainerMemMiB`
       and `maxContainerVCPU`, plus per-class `max_instances`.
     - Enforced by a **synchronous mgmt reservation at `start()`**
       (released on stop, reconciled from heartbeats on the store clock).
     - It **fails closed** when mgmt is unreachable, except when restarting
       an already-reserved sandbox.
     - Host-held budget slices can replace it with fast-vm.
   - **Requirements for smart-placement** (ADR-030, smart placement):
     1. tier and capability eligibility plus `arch`;
     2. the capacity vector;
     3. pool;
     4. a never-violated `colocateWith` / pin input;
     5. quota accounting hooks;
     6. headroom-aware parent placement for container-bearing workloads.

**v1 scope** (the smallest thing that runs both acceptance examples
unmodified):
- **Prerequisites:** amd64 microVM assets, and hostd refusing instead of
  falling back.
- **guestd container mode:** erofs + overlay, entrypoint child, exec with
  streamed stdio, files via exec.
- **hostd:**
  - the container VM lifecycle, keyed by object;
  - inactivity and hard-max timers;
  - `getTcpPort().fetch` proxying to the guest;
  - the sandbox egress proxy (deny-all, intercepts to the parent,
    allowlisted internet);
  - the capacity vector.
- **The wrapper polyfill** of the v1 `ctx.container` methods.
- **mgmt:**
  - the tsnet OCI registry (per-tenant repos, mirroring);
  - digest pinning in deployments;
  - `containers` and `arch` host capabilities;
  - container quotas and reservations.
- **CLI:** `tw deploy` builds the image locally and pushes it.
- **The vz backend** for the macOS microVM tier.
- **Tests:**
  - e2e: both examples; kill < 1 s; fence inheritance; deny-all egress
    (ADR-019 list plus the internet); the interceptor path; the strict
    keyscan.

**Deferred:**
- preview URLs (ADR-029);
- `getTcpPort().connect()` and raw TCP;
- `snapshotContainer` (v1.1);
- memory snapshots and fork;
- persistent sandbox volumes;
- bucket mounts and backups;
- the own-node opt-in;
- standalone (unattached) sandboxes;
- cosign;
- gVisor and cloud-hypervisor;
- GPU (non-goal);
- the upstream `containerEngine`.

**Consequences.**
- `harness/container/*` and the Sandbox SDK move toward ✅ with no
  Cloudflare-side changes.
- We track a partly specified Cloudflare surface: readiness, inactivity
  and intercept semantics.
- Running code by default adds a large untrusted surface to the
  microVM tier. The no-tailnet default keeps a prompt-injected sandbox's
  reach bounded by its parent's code.
- The vz backend and amd64 assets are large items that also benefit
  plain microVM DOs.

**Open.**
- Rosetta for Linux under vz with our erofs/guestd layout.
- Whether arm64 rebuilds of `cloudflare/sandbox` stay in lockstep with
  Cloudflare releases.
- Exact readiness and retry semantics that `@cloudflare/containers` 0.3.x
  expects from `start` / `getTcpPort`.
- Huge-page budgeting for multi-GiB sandboxes: 4K pages cost seconds of
  boot under nested virtualization (M8 finding 1).
