# Data-plane loop ledger: containers, pooled nodes and snapshot restore

Issue #423. Process: `../LOOP.md` ("Containers, pooled nodes and snapshot
restore"). Findings are filed as GitHub issues labelled `dataplane-chaos`.
Run it with `make e2e-dataplane-vm` (needs a Mac's virtualization or
Linux + KVM as root). Backends used so far:

- **vz**: this Mac (Apple silicon, Virtualization.framework, no sudo), 3
  hosts, `-pool-nodes 2`, the arm64 `tw.local/debian-trixie-python:1`.
- **Firecracker**: an amd64 Ubuntu 24.04 KVM guest (2 vCPU, 6 GiB),
  hostd as root with the in-process helper, 2 hosts
  (`TW_DPK_HOSTS=2 TW_DPK_MAX_OBJECTS=4 TW_DPK_BURST=2`), warm pool 1,
  snapshots on. The only backend with snapshot restore, so `restore` runs
  here.

## Status

| Scenario | Backend | Clean seeds (consecutive) | Notes |
|---|---|---|---|
| containers | vz | 6, 7, 8 | 1, 2 failed on K-F1; 3, 4 clean on an earlier checker; 5 failed on a checker false positive (a `destroy()` on a stopped host) |
| containers | Firecracker | (focused runs only) | seed 11 with `crash-hostd-vmm,kill-vmm`: K-F2 before the fix, 0 of 26 VMMs survived 5 crashes after it |
| pool | vz | 5, 6, 7 | 1, 3, 4 failed on the name-read-back checker artifact (K-F3, refuted); 2 clean |
| pool | Firecracker | 1, 2, 3 | |
| restore | Firecracker | 10, 11, 12 | 4–7 and 9 clean too; 8 failed once on K-F4 (2 reruns clean); the first run of 10 failed on a checker false positive |
| mixed | vz | 4, 5, 6 | 1, 2, 3 clean on an earlier checker |

"Clean" counts runs on the final checker with the fixes for K-F1 (#583,
merged) and K-F2 (#590) applied. The microVM-tier copies (K3) come from
the warm pool and snapshot restore on Firecracker only; vz cold-boots
them, so `restore` runs there. `mixed` and `containers` (3 hosts, takeovers
of stopped and cut-off hosts) ran on vz because the KVM guest has 2 vCPUs.
10-minute soak: vz `mixed` seed 2001 passed (2026-11-25, 32 faults; see LEDGER.md). Not yet: a 10-minute soak on Firecracker.

## Findings

| ID | Verdict | Invariant | Issue | Summary |
|---|---|---|---|---|
| K-F1 | FIXED (#583) | K4 (every ready pool node runs on a host) | #567 | When the lease moved, the old host's pool nodes stayed `ready` in mgmt with no host and their devices were never deleted: 4 per run in containers seeds 1 and 2. mgmt now kills another host's pool nodes when a deploy places the workload |
| K-F2 | FIX IN PR (#590) | K4 (no VMM outlives its hostd) | #587 | Linux, in-process helper: a crashed hostd's Firecracker VMs (and their netns, jails, cgroups) were never reaped, since the helper names them after hostd's pid and its successor only cleans its own. 18 of 18 VMMs outlived 4 crashes (containers seed 11, `crash-hostd-vmm`), 0 of 26 with the fix. 118 leaked netns on the test host |
| K-F3 | REFUTED | K2 (final MagicDNS name read back before ready) | #599 (closed) | Reports carried an empty name: the Durable Object reads `self()` before its node is ready (it runs first), and its report is held until ready (#331), so it arrives later with the old answer. With `self().status` in the report: 129 `ready` reports all named, the 2 empty ones `pending`. K2 now checks names of `ready` reports, and that mgmt never has a `ready` object without a name. #602 (a netd change) closed |
| K-F4 | CONFIRMED, needs a decision | K2 (a crash mid-adoption leaks nothing) | #610 | A pool node minted just before a hostd SIGKILL stays `minted` in mgmt forever: hostd hadn't saved it, and its random name is never asked for again (a live object against `maxObjects`, a minted key in custody). restore seed 8, 1 of 3 runs. Options in the issue (mgmt reaping is the smallest) |
| K-F5 | CONFIRMED, open | (exec answers; noted by K1) | #611 | Firecracker: of ~9 concurrent `up`s on a container just restarting after a host restart, 2 got exit 0 with empty stdout from `cat /proc/sys/kernel/random/boot_id` (restore seed 10). Not seen on vz. Needs investigation in the VM exec path |

## Harness notes (not product bugs)

- Images: tw-hostd pulls container images only from mgmt's registry
  (ADR-032 D3), so the test pushes `TW_DPK_IMAGE` from `TW_CTR_IMAGES` as
  a public base first (`pushPublicImage`), as `tw image push` does.
- The keyscan reads the guest's disks whole (minutes per container on vz):
  one container per run (`TW_DPK_KEYSCANS`).
- Killing an object from inside it (`ctx.tailnet.kill`) cut its own
  answer off (a plain 500). Retired objects are killed through mgmt's
  `DELETE /v1/objects/{id}`, as `tw kill` does, so the result is known.
- A host that lost the lease keeps its objects' records as `pending` (no
  node) to rejoin if the lease comes back; K4 counts only `ready` ones.
- The vm workload's first draft named a field `at`, which broke decoding
  its `/id` answers into `vmRep.At`: K3 compared no microVM copies in
  restore seeds 1–3. Fixed (`drawnAt`); restore seeds 4–6 compared 6, 18
  and 15 copies.
- Once, right after a hostd restart, an `up` got a 200 that wasn't the
  object's answer (no boot_id, no epoch) and was read as epoch 0 (a false
  stale answer, containers seed 11 on Firecracker). Such a 200 is now an
  error, and K1 notes it with its body; not seen since.
- `crash-hostd-vmm` on vz: the VMMs (tw-vz) die with hostd (0 survivors
  in every run), so K-F2 is Linux only.
- K1's fence bound is 45 s (FenceAfter 30 s + a heartbeat + slack) from
  the later of the lease leaving the host and the end of the last fault on
  it. No container of a non-holder ran past it in any run.

## Run log

| Run (results dir or log) | Backend | Scenario / seed | Verdict |
|---|---|---|---|
| results/20261125T062820Z-k-containers-1 (worktree) | vz | containers 1, 1 min | HARNESS-BROKEN: keyscan of every container took minutes (now one per run) |
| results/20261125T063445Z-k-containers-1 (worktree) | vz | containers 1 | K4 fail: K-F1 (4 pool nodes), plus a checker false positive (pending host records) |
| results/20261125T064001Z-k-containers-2 (worktree) | vz | containers 2 | K4 fail: K-F1 (4 pool nodes); a kill during a cut not applied (harness: kills now go through mgmt) |
| /tmp/dpk-l1.log | Firecracker | restore 1, 1 min | pass (K3 compared no copies: the `at` field bug) |
| /tmp/dpk-l2.log | Firecracker | restore 2 | K4 fail: a VMM outside every hostd → K-F2 |
| /tmp/dpk-l3.log | Firecracker | restore 3 | pass (no copies compared) |
| /tmp/dpk-l4.log | Firecracker | containers 11, `crash-hostd-vmm,kill-vmm` | K4 fail: 18 of 18 VMMs outlived 4 crashes → K-F2 (#587) |
| results/20261125T072404Z-k-containers-11 | Firecracker + fix-587 | containers 11, same faults | K4 pass (0 of 26 survived 5 crashes); K1: a 200 without the object's report read as epoch 0 (harness: now an error, noted) |
| results/20261125T073143Z-k-containers-3 (worktree) | vz + fix-567 | containers 3 | pass |
| results/20261125T073349Z-k-restore-4 | Firecracker | restore 4 | pass, 6 copies compared |
| results/20261125T074128Z-k-restore-5 | Firecracker | restore 5 | pass, 18 copies (pool 4, restore 8) |
| results/20261125T074942Z-k-restore-6 | Firecracker | restore 6 | pass, 15 copies |
| results/20261125T073728Z-k-pool-1 | vz | pool 1 | K2 fail: 18 "misnamed" → K-F3, refuted |
| results/20261125T074311Z-k-mixed-1 | vz | mixed 1 | pass |
| results/20261125T074924Z-k-containers-4 | vz | containers 4 | pass |
| results/20261125T075612Z-k-pool-2 | vz | pool 2 | pass |
| results/20261125T080220Z-k-pool-3 | vz | pool 3 | K2 fail: K-F3 artifact |
| results/20261125T080753Z-k-mixed-2 | vz | mixed 2 | pass |
| results/20261125T081337Z-k-containers-5 | vz | containers 5 | K1 fail: `destroy()` 57 s on a SIGSTOPped host (checker false positive, fixed) |
| results/20261125T081910Z-k-mixed-3 | vz | mixed 3 | pass |
| results/20261125T082439Z-k-pool-4 | vz | pool 4 | K2 fail: K-F3 artifact |
| results/20261125T081720Z-k-pool-1 | Firecracker | pool 1 | pass; `self().status` in reports refutes K-F3 |
| results/20261125T082557Z-k-pool-2 | Firecracker | pool 2 | pass |
| results/20261125T083423Z-k-pool-3 | Firecracker | pool 3 | pass |
| results/20261125T083811Z-k-containers-6 | vz | containers 6 | pass |
| results/20261125T084417Z-k-mixed-4 | vz | mixed 4 | pass |
| results/20261125T084953Z-k-pool-5 | vz | pool 5 | pass |
| results/20261125T084306Z-k-restore-7 | Firecracker | restore 7 | pass |
| results/20261125T085523Z-k-containers-7 | vz | containers 7 | pass |
| results/20261125T085137Z-k-restore-8 | Firecracker | restore 8 | K2 fail: a pool node stuck `minted` → K-F4 (#610) |
| results/20261125T090127Z-k-mixed-5 | vz | mixed 5 | pass |
| results/20261125T085938Z-k-restore-9 | Firecracker | restore 9 | pass |
| results/20261125T090658Z-k-pool-6 | vz | pool 6 | pass |
| results/20261125T090824Z-k-restore-8, …T091707Z-k-restore-8 | Firecracker | restore 8 (reruns 1, 2) | pass, pass: K-F4 depends on the crash timing |
| results/20261125T091228Z-k-containers-8 | vz | containers 8 | pass |
| results/20261125T091759Z-k-mixed-6 | vz | mixed 6 | pass |
| results/20261125T092339Z-k-pool-7 | vz | pool 7 | pass |
| results/20261125T092517Z-k-restore-10 | Firecracker | restore 10 | K1 fail: `destroy()` of a container still booting after a host restart (checker false positive, fixed); notes: K-F5 (#611) |
| results/20261125T093515Z-k-restore-10 | Firecracker | restore 10 (rerun, final checker) | pass; notes: K-F5 again (3 empty boot_id answers) |
| results/20261125T094340Z-k-restore-11 | Firecracker | restore 11 | pass, 15 copies |
| results/20261125T095140Z-k-restore-12 | Firecracker | restore 12 | pass, 18 copies (pool 2, restore 7) |
