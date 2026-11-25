# ADR-030: microVM snapshot restore: one shared base per host, private guest pages, no cross-tenant page sharing, hourly base rotation

**Date:** 2026-11-13
**Status:** Accepted (Ross, 2026-11-13; implemented on tailworks branch `fast-vm`):
1. one base shared by all tenants on a host;
2. no guest pages shared across tenants, ever (no File backend, no KSM);
3. the base is rebuilt at every hostd start and every hour (`-vm-snapshot-rotate`).

`-vm-snapshot` is **on by default**, used for warm-pool misses. The pool still refills by booting: restore-filled pool VMs were measured 20–50 ms slower to claim; `-vm-snapshot-refill` opts in to restore-filled refills.

**Context.**
- **Cold boot vs restore.** A cold microVM boot to workerd-ready takes ~0.6 s (tailworks STATUS FV Phase 1; nested virt). Restoring a Firecracker snapshot avoids the kernel boot and most first-touch costs.
- **What's built (tailworks `hostd/vm/snapshot_linux.go`, `helper/uffd_linux.go`):**
  - **The base.** hostd boots one *pooled* VM: kernel, guestd, and a no-tenant warm-up workerd that has run once and exited. It has an empty app dir, no tenant code, no keys and no instance identity (every VM uses the same guest IP, MAC and vsock CID, each in its own netns and jail; identity comes with the post-restore `load`). The helper (root) takes a full snapshot of it into a root-only directory (state 11 KB, memory 512 MiB, config drive 8 KB) and kills it, so the original never resumes. The base lives as long as hostd's helper connection.
  - **A restore** is a new jail, netns and VMM, plus `PUT /snapshot/load` with `mem_backend: Uffd`. Firecracker v1.17 requires Uffd for 2 MiB pages. The **helper** serves the faults: it copies each touched page from the base memory file into the VM with `UFFDIO_COPY`, and prefetches the base's non-zero pages in the background. **Firecracker never maps the memory file; every restored VM gets private copies of every page.**
  - **After the restore**, guestd reconnects and is claimed like a pool VM. Its `load` brings the bundle, 64 bytes of host entropy (then `RNDADDENTROPY` + `RNDRESEEDCRNG`), a new boot_id and the wall clock. The tenant's workerd then starts as a **new process**, after the reseed.
- **Measured** (nested virt, Lima on an M5 Pro; no bare-metal numbers):
  - on-demand restore → workerd ready p50 100 / p95 125 ms (the restore itself: 38 / 61 ms);
  - a pool refilled by restore + re-warm: claim → ready p50 28 / p95 34 ms;
  - cold boot: ~600 ms; boot-refilled pool: 58 ms with a volume.
- **Tested:**
  - **Uniqueness:** 3 concurrent copies differ after `load` in kernel getrandom, `/proc` UUIDs, boot_id, and workerd's `crypto.getRandomValues`, `randomUUID` and `Math.random`. Wall clock and workerd `Date.now()` are within 50 ms of the host's (before the load, a copy's wall clock lags by the base's age; its monotonic clock does not jump). Each copy's egress reaches only its own gateway.
  - **No keys:** the 26+ needle scan (tskey-, auth key, every node and machine private key, the volume key) finds nothing in a restored VM's disks, files or full RAM, nor in the base snapshot's three files. The workload's MAC key is in the restored VM's RAM (positive control) and **not** in the base.
- **Firecracker's own guidance** (`docs/snapshotting/random-for-clones.md`, v1.17):
  - it considers resuming one state more than once insecure;
  - VMGenID on aarch64 needs a guest kernel ≥ 6.10 (DeviceTree), and our guest kernel is 6.1, so tailworks reseeds explicitly.

**Threat model.**
- **Adversary:** a tenant's code in its own VM, up to and including a V8 escape and a guest-kernel compromise. It wants another tenant's data, keys, randomness or activity, or another tenant's identity.
- **Out of scope:** a VMM escape (jailer, seccomp) and a host compromise.
- **What restore must not leak:**
  - (a) secrets that sit in the base: there are none, by construction, and the scans test it;
  - (b) shared randomness: reseeded per copy before tenant code, and tested;
  - (c) another VM's activity through shared physical memory;
  - (d) cloned kernel state that is never re-randomized.
- **(c) is the decision below.** Copies of one base *could* share read-only physical pages: the Firecracker `File` backend `MAP_PRIVATE`-maps the memory file into every VMM, or KSM could merge them. Those pages hold the guest kernel, workerd/V8 and BoringSSL code, all identical across tenants.
  - **Attacks this enables:** Flush+Reload or Prime+Probe on shared code pages (Yarom & Falkner 2014, cross-VM via KSM), and copy-on-write timing. Together these let one tenant observe *which shared code paths another tenant's VM executes*, e.g. the crypto routines in TLS, or simply when it is active.
  - The base's content is not secret; the access pattern is.
- **(d) Cloned kernel state.** These stay identical in every copy of a base:
  - the guest's KASLR slide and early-boot hash/siphash keys;
  - `net_secret` (TCP ISN and syncookie keys);
  - guestd's (Go) runtime seeds and stack canaries.

  A tenant that learns its own values knows them for every co-resident copy. Using them needs a second primitive that reaches into *another* guest; guests have no network path to each other (each one's only peer is its own host stack). So the residual risk is low, but it is not zero.

**(d) analysed (2026-11-21, issue #157, ledger H24): shared, not exploitable across tenants, disclosed.**
- **What the copies share** (guest kernel 6.1). The load's `RNDRESEEDCRNG` changes what is drawn from the CRNG *after* it. It does not re-key what the kernel drew *before* the snapshot and keeps for its lifetime:
  - `net_secret`: the TCP ISN (`secure_tcp_seq`: siphash of the 4-tuple, plus the clock) and the ephemeral-port offset (`secure_ipv4_port_ephemeral`, with the port-perturbation table);
  - `ts_secret` (per-flow TCP timestamp offsets), the syncookie secrets, the established/UDP hash-table secrets (`inet_ehash_secret` and similar), and the IP-ID hash key;
  - boot-time siphash/jhash seeds of kernel hash tables, the slab freelist random sequences, the stack canaries of kernel threads started before the snapshot;
  - guestd's Go runtime seeds (map hash seeds, `math/rand`). guestd's `crypto/rand` calls `getrandom` on every read, so it is post-reseed.

  Most of the networking secrets are created lazily (`net_get_random_once`) at first use. They are cloned if the base used them before the snapshot, and unique if their first use is after the load. `vm.TestSnapshotKernelState` (linux + KVM) measures which ones are cloned. In 3 restored copies and a cold-booted control, it reads the ISN with its clock term removed, the timestamp offset, the first ephemeral port, `_text` and the TFO key. Measured on 2026-11-22 (amd64, a nested KVM test host): the 3 restored copies share `net_secret` (the ISN key), `ts_secret`, the first ephemeral port, `_text` and the TFO key. The cold-booted control differs on the ISN key, the timestamp key and the port, and has the same `_text` (no slide, see below). So the worst case this analysis assumes, that all of them are shared, is what happens. The verdict below doesn't depend on the measurement.
- **What they don't share:** anything drawn from the CRNG after the load. That covers the tenant's workerd (a new process: V8's hash seed, BoringSSL, `Math.random`), every per-socket random value (the IP ID of a DF socket, the source-port step), the stack canaries of new processes (set at fork) and `getrandom`.
- **KASLR barely matters here.** On amd64, Firecracker loads the uncompressed `vmlinux` directly, so the guest kernel gets no KASLR slide ("Kernel Offset: disabled" on the amd64 guest console). Every guest of one kernel build has the same layout, booted or restored. On arm64 any slide is fixed at the base's boot and shared by its copies. Either way it helps only an attacker who already has a kernel bug to exploit in a guest it can reach.
- **Why it isn't exploitable across tenants:**
  - Predicting an ISN or a port is useful only to someone who can see or inject into the victim's TCP. A guest's TCP never leaves its host. It ends at its own VM's gVisor gateway stack in hostd (`hostd/vm/vnet.go`), which accepts only the gateway's platform ports. netd opens its own connections from the object's node, and every other flow is reset (`vm.TestDgramNICOnlyGatewayServices`, the shared stack code). Guests have no path to each other: each is in its own netns with its own TAP.
  - So a tenant can't observe another copy's sequence numbers, ports or timestamps. It can't inject into another copy's connections either: the only peer of each is that copy's own gateway stack.
  - **Hash flooding** needs the attacker to choose the keys that are hashed. Tenant-controlled input reaches only userspace tables in the victim's workerd, which are seeded after the reseed. The victim's kernel TCP tables hold 4-tuples picked by its own kernel and its own gateway stack; public requests reach netd at L7. guestd hashes only what the host sends.
  - **What is left:** a tenant (with a guest-kernel compromise) can learn values that every copy shares, for the base's lifetime (≤ 1 h, decision 3). That is a step only in a chain that already crosses the VM boundary. After a VMM escape (out of scope) the attacker is on the host and doesn't need it.
- **No rotation on load.** Linux has no sysctl or ioctl that re-keys `net_secret`, `ts_secret` or the hash-table seeds after boot; that needs a guest kernel patch, which tailworks doesn't build. The TCP Fast Open server key is writable (`net.ipv4.tcp_fastopen_key`), but guests run no TFO server, so rotating it would change nothing. The amd64 kernel has `CONFIG_VMGENID`, which only reseeds the CRNG.
- **Revisit if** a guest ever gets an L3 path that leaves the host or reaches another guest (NAT or bridged egress, raw UDP, guest-to-guest networking). Then use option 2 (a base per tenant) or patch the kernel to re-key on load. The fix would not be "reseed harder".

**Options.**
1. **One shared base per host, private pages (built).**
   - Pros: no cross-VM physical page sharing (the host page cache of the memory file is read only by the helper), and no per-tenant base cost (boot + snapshot ~0.9 s, 512 MiB of disk).
   - Cons: (d) is shared across tenants; restore-fault timing could hint that *a restore happened recently*. The page-cache warmth of the base memory file is host-wide, and prefetch mostly hides it.
2. **One base per tenant (or per trust domain: `isolation` tenant / principal / object, RFC §4.8), private pages.**
   - Pros: (d) is no longer cross-tenant.
   - Cons: N × (0.9 s, 512 MiB disk) per host; the base still holds no tenant code, so the content is the same and only the cloned *randomized* state differs.
3. **Shared pages (File backend or KSM) within a trust domain only.** Cuts resident memory by up to ~106 MiB per VM (the base's resident set). It needs 4 KiB guest pages (`File` cannot do 2 MiB), which were ~8× slower to start under nested virt (M8 finding 1). It must never cross tenants.
4. **Shared pages across tenants.** Rejected: (c).
5. **Rotate the base** (rebuild every N restores or T minutes). This bounds (d) in time and composes with 1 or 2; a rebuild costs ~0.9 s of background work.

**Recommendation (for Ross to accept or change).**
- **Option 1 with option 5:** one shared base per host; private copies only; **never share guest physical pages between tenants** (KSM stays off for VMM memory); rebuild the base on a timer (e.g. hourly, plus at every hostd start, as today).
- Move to option 2 when `isolation: "object"` (one VM per object) ships for untrusted code, if (d) matters for that tier.
- Keep the flag **off** until this is decided. After that, on by default for hosts that run the microVM tier.

**Decision needed.**
1. ~~Is one base shared by all tenants on a host acceptable, given private pages and cloned-but-unshared kernel randomization (d)? Or one base per tenant / trust domain?~~ **Decided (Ross, 2026-11-13): one shared base per host.** Residual risk (d) is accepted. This is what's built.
2. ~~Confirm: no guest-page sharing across tenants, ever (no File backend, no KSM, across tenants).~~ **Decided (Ross, 2026-11-13): confirmed.**
3. ~~Base rotation: required, and how often?~~ **Decided (Ross, 2026-11-13): yes, at start and hourly.** On rotation, idle pool VMs restored from the old base are discarded, and its files are removed after a 30 s grace.

**Open.** Bare-metal numbers. A per-restore KASLR re-randomization isn't possible with snapshots (and direct `vmlinux` boot has no slide to re-randomize); accepting (d) is the price of restore. `net_secret` could be re-keyed by a guest kernel patch, which tailworks doesn't build; (d) is analysed above and disclosed in `docs/security-model.md`.
