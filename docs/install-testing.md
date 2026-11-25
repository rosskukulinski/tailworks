# Where the install is tested, and what was measured

[INSTALL.md](INSTALL.md) is the install guide. This page is the evidence
behind it: which machines the install steps ran on, the harness that ran
them, and the timings measured there. The full history is in
[STATUS.md](../STATUS.md) (frozen at 2026-11-14) and [status/](../status/)
(one file per workstream since).

## macOS

The [macOS steps](INSTALL.md#macos) were run as written, on a clean
Apple-silicon Mac with nothing installed (`mbp`, M2 Pro, macOS 26.6), by
`make e2e-mbp`. The measured numbers are in STATUS.md, "M6: release
packaging + E2E-MAC on a different, clean Mac".

## Linux requirements

Linux is a host platform on a par with macOS. These are the requirements
the Linux work was built and tested against; code comments cite them by
number.

1. **Releases:** `linux-arm64` and `linux-amd64` tarballs, both built and
   tested. `install.sh` installs into `~/.tailworks` without root, on any
   supported distro.
2. **E2E-LINUX:** the full e2e flow (checks a–f, plus multi-tenant
   isolation) passes from the installed release on fresh VMs: Ubuntu 24.04
   arm64, Debian 12 arm64, Ubuntu 22.04 arm64, Ubuntu 24.04 amd64 (process
   tier only) and Fedora arm64. The test client and echo run on another
   machine over the test tailnet (`make e2e-linux`; results in the
   [support matrix](#linux-support-matrix)).
3. **Process-tier sandbox:** each tenant's workerd and Node app runs under
   Landlock (unprivileged), plus seccomp where cheap, with the same
   guarantees as the macOS Seatbelt profile and the same cross-tenant
   attack tests. Without Landlock, `tw doctor` warns and multi-tenant use
   is refused unless `--insecure-shared-host` is passed.
4. **microVM tier:** Durable Object storage persists across VM restart and
   redeploy; Firecracker's jailer is used; one-time
   `sudo tw host setup --microvm` is the only privileged step, and without
   it tailworks falls back to the process tier and `tw doctor` explains.
5. **Service management:** `tw up --service` installs a systemd *user* unit
   so tailworks survives logout (with `loginctl enable-linger`) and reboot;
   `tw down --service` removes it.
6. **`tw doctor` on Linux** checks glibc, the kernel and Landlock ABI,
   `/dev/kvm`, the jailer, cgroups v2, systemd, and that the system
   tailscaled is not used, with clear errors for unsupported hosts
   (musl/Alpine, glibc < 2.35, no Landlock).
7. **[INSTALL.md](INSTALL.md#linux)** covers Linux as a peer of macOS, with
   per-distro notes and the measured support matrix below.

## Linux support matrix

`make e2e-linux`, measured on **2026-11-22** with **OAuth-only minting** and
the **Landlock sandbox on**, on two builds:

- **`v0.3.0`**, the release tarballs from GitHub. On that date v0.3.0 was
  still a **draft** release (tag pushed, signed assets, not yet marked
  published). The tarballs were checked against its `SHA256SUMS`, and
  `install.sh` checked the signature in each VM
  (`make e2e-linux LINUX_RELEASE=v0.3.0`).
- **`0.3.0-45cd9203`**, a local `make release` of master after #228 (the
  pool-node rejoin fix), unsigned, without the microVM guest. The numbers
  in the table come from this build.

Every VM was **fresh**: a Lima official template, 4 CPU / 6 GiB / 30 GiB,
`--plain --mount-none`, and nothing mounted from the Mac. The tarball and
the run's own OAuth client file (0600) were copied in with `limactl copy`.
tailworks got no API key. The harness keeps the API key on the Mac to
create and delete that OAuth client and to clean up. tailworks ran in the
VM under `tw up --service`. The tag:ttest client and echo ran on the Mac
(Apple silicon) over the test tailnet. Every passing row passed checks
(a)–(f), the pre-joined pool node (a2), `tw doctor` before and while
running, the sandbox check (every process-tier workload under
`process/landlock`), `systemctl --user restart tw`, `tw down --purge
--service` and cleanup: zero leftover devices or keys, and the VM clean.
**microVM tier: unavailable on every row** (no `/dev/kvm` in these VMs).
Raw results: `e2e/real/results/linux-113-*/` (gitignored). The older
2026-11-08 run (local build `0.1.0-bb3a3296`, API-key minting, no
sandbox) is in STATUS.md, "L1" and "Final verification".

| | Ubuntu 24.04 arm64 | Debian 12 arm64 | Ubuntu 22.04 arm64 | Fedora 45 arm64 | Ubuntu 24.04 **amd64** (qemu, emulated) |
|---|---|---|---|---|---|
| measured | 2026-11-22 · OAuth · sandbox on | 2026-11-22 · OAuth · sandbox on | 2026-11-22 · OAuth · sandbox on | 2026-11-22 · OAuth · sandbox on | 2026-11-22 · OAuth · sandbox on |
| `v0.3.0` (draft release) | **FAIL**: reboot phase, 3 of 4 objects rejoined (the idle pool node didn't; fixed in #228); every earlier check passed | **PASS** | **PASS** | **PASS** | **PASS** |
| `0.3.0-45cd9203` (master, local build) | **PASS** (with the reboot and `--ephemeral` phases) | **PASS** | **PASS** | **PASS** | **PASS** |
| glibc · kernel · Landlock ABI | 2.39 · 6.8 · 4 | 2.36 · 6.1 · 2 | 2.35 · 5.15 · 1 | 2.44 · 7.2 · 10 | 2.39 · 6.8 · 4 |
| doctor `sandbox` | ok | warn (ABI 2: TCP by seccomp only) | warn (ABI 1: filesystem rules + seccomp) | ok | ok |
| `install.sh` | 0.38 s | 0.94 s | 1.05 s | 0.94 s | 10.4 s |
| `tw up --service` → ready | 5.98 s | 6.26 s | 6.25 s | 6.03 s | 16.1 s |
| `tw deploy` → ready (agent / mcp) | 1.46 / 1.66 s | 1.44 / 1.64 s | 1.38 / 1.41 s | 1.64 / 1.78 s | 7.19 / 3.35 s |
| first request → object node ready (new node / pre-joined pool node) | 1.26 s / 209 ms | 1.21 s / 212 ms | 1.24 s / 300 ms | 1.25 s / 238 ms | 3.05 s / 2.54 s |
| `tw kill --cascade` (CLI / push) | 81 / 62 ms | 112 / 81 ms | 95 / 68 ms | 81 / 61 ms | 530 / 175 ms |
| request to a killed object → 410 | 1.0 ms | 11.4 ms | 1.2 ms | 1.3 ms | 8.0 ms |
| Mac → VM path | **direct** after 0.18 s | **direct** after 0.18 s | **direct** after 0.20 s | **direct** after 0.19 s | **direct** after 0.55 s |
| MCP `tools/list` direct · DERP-only (median) | 3.9 · 13.4 ms | 2.4 · 8.9 ms | 2.4 · 10.0 ms | 3.0 · 11.1 ms | 41.0 · 43.2 ms |
| disco ping direct · DERP (median) | 1.01 · 8.8 ms | 0.65 · 8.9 ms | 0.62 · 8.0 ms | 0.71 · 9.1 ms | 1.44 · 8.4 ms |
| `systemctl --user restart tw` → healthy / workload answers the Mac | 7.9 / 10.9 s | 8.1 / 15.1 s | 8.5 / 11.6 s | 2.9 / 5.9 s | 16.4 / 23.9 s |
| `tw down --purge --service` | 6.69 s | 6.81 s | 6.71 s | 1.74 s | 13.6 s |
| reboot (enable-linger, `limactl stop`/`start`) → service active / healthy / workload answers | 5.6 / 36.6 / 47.1 s | — | — | — | — |
| `--ephemeral` sub-agent across `systemctl --user restart` → re-minted, ready / answers | 6.8 / 6.9 s | — | — | — | — |
| in `make e2e-linux`'s default rows | yes | yes | yes | yes (since 2026-11-22) | yes |

The direct path is the Mac ↔ Lima VM NAT (a private address); DERP-only
numbers come from a second client with `TS_DEBUG_ALWAYS_USE_DERP=1`. The
amd64 column ran under qemu x86-64 emulation on an Apple-silicon Mac, so
its timings are slower than a real x86 host's.

**Slower than on 2026-11-08** (#245): `tw up --service` (about 6 s, was
0.7 s), `tw down --purge --service` (about 6.7 s, was 0.6 s), the
`systemctl --user restart` command itself (5.2 s, was 3.1 s), and reboot →
healthy (36.6 s, was 6.6 s). On every stop, mgmt exits about 5 s after
hostd. The v0.3.0 rows show the same numbers. A re-run of the Ubuntu 24.04
arm64 row on `0.3.0-cf0e5c16` (master with #231 and #232) passed:
`tw down` took 3.6 s, the restart command returned in 1.1 s, and reboot →
healthy was still 37.6 s. Of the 42 warm keys mgmt minted during that run,
none was still valid after `tw down`; the harness now checks this. After
#250, the same row on `0.3.0-0401f5c4` (2026-11-22) passed with
`tw down --purge --service` at **0.60 s**, back to the 2026-11-08 figure.
`tw up --service` took 5.8 s, the restart command 5.3 s, and reboot →
healthy 37.4 s, all still open in #245. One Fedora run (master build)
finished the restart command in 0.2 s and `tw down` in 1.7 s; its v0.3.0
run took 5.2 s and 6.7 s like the others.

Unsupported hosts (`TestUnsupportedHost`; 2026-11-22, both builds; no
tailnet or minting involved):

| | Debian 11 arm64 (glibc 2.31, kernel 5.10) | Alpine 3.24 arm64 (musl, kernel 6.18) |
|---|---|---|
| result (v0.3.0 / master) | **PASS** / **PASS** (refused as designed) | **PASS** / **PASS** (refused as designed) |
| `install.sh` | refused: `glibc 2.31 is too old: tailworks' workerd needs glibc >= 2.35 (Ubuntu 22.04+, Debian 12+, Fedora 36+, RHEL 10)` | refused: `this Linux uses musl libc (Alpine?); tailworks' workerd needs glibc >= 2.35 …` |
| `bin/tw doctor` | `✗ glibc` (names the distro and the fix); `! kernel` (< 5.13); `! landlock` (ENOSYS) | `✗ glibc`: `this system uses musl libc (/lib/ld-musl-aarch64.so.1) …`; `✗ sandbox` (workerd can't start) |

RHEL/Alma/Rocky 8–9 and Amazon Linux 2 were not run in the matrix; they are
refused for their glibc version.

**Lima notes.** Debian 11's genericcloud image doesn't boot under Lima's
`vmType: vz`; use qemu. To run the matrix on a published release:
`make e2e-linux LINUX_RELEASE=vX.Y.Z` (downloads its linux tarballs and
checks them against its `SHA256SUMS`). Add `LINUX_APPLY_POLICY=1` to first
add the scenario's tagOwners and grants to the test tailnet's policy, and
`TW_E2E_VM_PREFIX=twdl-` to reuse VMs named before the rename.

## Restarts and reboots

Measured on Ubuntu 24.04 arm64 (the matrix's first column, 2026-11-22,
master build):

- **`--ephemeral` nodes across a restart:** a spawned sub-agent was ready
  again 6.8 s after `systemctl --user restart tw`, and answering a client on
  another machine after 6.9 s.
- **Reboot, lingering on:** from the start of boot, ssh up and `tw.service`
  active at 5.6 s, tailworks healthy at 36.6 s (#245), every object
  rejoined from saved state (same node IDs, nothing re-minted, the idle
  pool node included), and the workload answered a client on another
  machine at 47.1 s. On 2026-11-08 (API-key minting, no sandbox) healthy
  came at 6.6 s and the workload answered at 8.6–17.1 s.

The client-side part of the spread: a persistent node comes back with the
same node key but fresh WireGuard state, so a client that still holds a
recent session to it keeps sending on that dead session, and WireGuard only
re-handshakes after ~15 s of silence. A client whose session had already
gone idle, or a client that has never talked to the node, reaches it within
2–3 s of `tw up`. The node is reachable over DERP the moment it is up. The
diagnosis is in STATUS.md, "Diagnosis: the 12–17 s "client re-finds the
node after reboot" delay".

## Fast identity

How fast a new object's own node comes up with the
[tuning](concepts.md#tuning-how-fast-a-new-objects-own-node-comes-up)
defaults (warm keys, pool nodes, no port mapping), measured on a production
tailnet: the first request on the object's own node arrives ~0.23 s after
it is created (p95 ~0.3 s), down from ~7 s. The breakdown, and the numbers
before each step, are in
[ADR-031](design/adr/031-pre-joined-node-pool.md).

## The microVM tier

- **Tested end to end** on Ubuntu 24.04 arm64 (nested virtualization).
  `make e2e-linux-vm` and `make e2e-linux-reboot` ran from the shipped
  tarball; the rows are in STATUS.md, "Final verification".
- **amd64:** verified on 2026-11-22 on a nested KVM test host (an Ubuntu
  24.04 guest, not bare metal), at commit 45cd920:
  - `make e2e-vm` passes: every `hostd/vm` test (pool; snapshot restore,
    pool, uniqueness and rotation; smoke; volume; datagram NIC),
    `TestE2EVMKV`, `TestE2EVMR2`, `TestE2EVM` (14/14) and
    `TestE2EVMSnapshot` (14/14).
  - Cold boot (`TestSmoke`): boot 0.70 s, ready 0.92 s.
  - Snapshot restore (`TestSnapshotRestore`): on-demand restore p50 93 ms,
    p95 130 ms; creating the base snapshot takes 1.9 s.
  - Not run on amd64 yet: `make e2e-container`, and a separately installed
    `tw-vmhelper` daemon.
- **Fast starts** (the warm pool and snapshot restore): the measured times
  are in [ADR-030](design/adr/030-microvm-snapshot-restore.md).
- **A tarball without the guest.** The red team's second pass found a
  Mac-built tarball without the microVM guest. `make release` now builds
  the guest through Lima on a Mac, or warns, and the GitHub release
  workflow refuses to publish without it (see
  [INSTALL.md](INSTALL.md#linux-the-microvm-tier-firecracker-kvm)).

## Safety rules for the real-tailnet suites

The suites that join real tailnets (`e2e-real`, `e2e-mac`, `e2e-mbp`,
`e2e-multi`, `e2e-linux`, `e2e-oauth`, `e2e-public` and the like) follow
these rules, and new ones must too:

1. **Dedicated test tailnets only.** Each suite allow-lists the tailnets it
   may touch and refuses any other. Never point one at a personal or
   production tailnet, and never use the machine's system `tailscaled` or
   Tailscale app: tailworks' own nodes are tsnet nodes.
2. **Policy changes are additions only,** validated before they are
   applied, applied with `If-Match`, and only after a copy of the previous
   policy has been saved next to the credentials. Record each change in
   your `status/<branch>.md`.
3. **Never print keys** (auth keys, OAuth secrets, node keys) in test
   output, logs or status files.
4. **Clean up what you created:** delete your own devices and revoke your
   own keys by ID, even when the test fails.
