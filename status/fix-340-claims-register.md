Last update: 2026-11-24 PT. Issue #340: the five isolation claims from the Cloudflare review are in the claims register, each with the test that drives its production path.

## What this branch does

Tests only, no production code. For each claim in #340's table the ledger
(`security/LEDGER.md`, claims register) now has a row that names the test
pinning it, per LOOP.md hard rule 8 (the test drives the handler, boot path
or generated command line tailworks runs, not only a helper).

| Claim | Row | New or existing test |
|---|---|---|
| workerd's inspector is never enabled on a host | C-inspector | new: `hostd.TestInspectorOffOnHost` (real hostd + workerd: the running workerd's command line has no `--inspector-addr`; `InspectorAddr` is empty), `hostd.TestInspectorArgsNeedBothDevFlags` (`workerdCommand` matrix: a host with `WorkerdInspector` alone still gets none) |
| A host's image cache is per tenant | C-imagecache | new: `hostd.TestContainerStartRefusesAnotherTenantsPin` (the container start path with A's image already in the cache; B's pin of A's repository and digest is refused before any fetch or boot; B's identical bytes are a separate pull into B's scope). Existing: `mgmt.TestRegistryAccess` (host pulls through `TailnetHandler`), `mgmt.TestDeployPinsDigests`, `hostd.TestContainerRegistryPull` (deploy refusal), `registry.TestPinAliasAndArches` (`tw.local/<other tenant>/…` resolves only in `public/`) |
| Scratch disk is new per boot, never reused | C-scratch | new: `vm.TestContainerScratchFresh` (KVM: marker written in container 1 is on its own scratch file and nowhere after; the jail root is removed with the VM; container 2's scratch file holds none of it), `helper.TestMakeScratchNeverReusesAFile` (Linux, no root: `O_EXCL`) |
| Container VM always cold-boots | C-coldboot | existing: `vm.TestContainerSmoke` → `checkCtrColdBoot` (pool and snapshots on); the new `vm.TestContainerScratchFresh` runs the same check on both of its boots |
| DO volume key released only to the lease holder | C-volkey | existing: `mgmt.TestAPIVolumeKey` (the `/v1/volumes/{id}/keys` handler, spec-validated: another host and a stale epoch get `409 lease_lost`), `vm.TestVolume` (KVM: wrong key refused, no plaintext in the image, key in no guest memory) |

None of the five turned out overstated.

## Tests

Commands and results are listed below as they were run; the KVM section is
updated after each run.

- `go test ./hostd -run 'TestInspectorArgsNeedBothDevFlags|TestInspectorOffOnHost|TestContainerStartRefusesAnotherTenantsPin' -count=1 -v` (macOS, after `make deps`): PASS (the end-to-end inspector test skips without `node_modules/.bin/workerd`, so run `make deps` first).
- `GOOS=linux GOARCH=amd64 go vet ./hostd/vm/ ./hostd/vm/helper/`: ok.
- `make changes-check`: ok (no upgrade-relevant paths changed; no fragment needed).
- On the amd64 nested-KVM test host (Ubuntu 24.04, Firecracker v1.17, Go 1.27.4), worktree rsynced to `~/tw-fix-340/`:
  - `go test ./hostd/vm/helper -run TestMakeScratchNeverReusesAFile -count=1 -v` (unprivileged): PASS (0.24 s).
  - `go test -c -o bin/vmsmoke.test ./hostd/vm`, then as root with the `make e2e-container` environment (`TW_VM_KERNEL` and `TW_VM_CONTAINER_KERNEL` = the amd64 guest kernel build with erofs, `TW_VM_ROOTFS` = the amd64 guest rootfs built 2026-11-22, `TW_CTR_IMAGES` = a copy of the amd64 Sandbox image index with `tw.local/` keys; the in-process helper, the warm pool and snapshots on, 1024 huge pages):
    - `bin/vmsmoke.test -test.v -test.count=1 -test.run 'TestContainerSmoke|TestContainerScratchFresh'`: `TestContainerSmoke` PASS (32.6 s; cold boot next to a pool of 2, claims 0, misses 0). The first `TestContainerScratchFresh` run reached the last step and hung for the 14 min cap in a `grep -r` over `/` inside container 2 (it walks `/proc` and `/dev`); the host-side scan of container 2's 3815 MiB scratch file had already passed. The grep now covers the writable dirs only.
    - `bin/vmsmoke.test -test.v -test.count=1 -test.run TestContainerScratchFresh` (fixed): PASS (23.0 s). Container 1's marker was in its own `scratch.ext4`; after Stop the jail root was gone; container 2 got the same slot and the same path, a new file, with no trace of the marker (raw scan of the file and from inside).
  - The two KVM rows are `pinned (KVM)` on that run. The macOS vz variant of the scratch test was not written (needs Apple silicon with the guest image; noted in the row).
- `make lint`: ok.
- `sh ~/.claude/scratch/mbp-test.sh test` (make test offloaded to the mbp runner): exit 2; every package passed except `mgmt`, where `TestWarmKeyRefillRespectsThrottle` (2 keys minted within ~3 s at 10/min) and `TestWorkloadReplicas` (`503 no_host`: h1–h3 "not live" after a 23 s heartbeat gap) failed under the runner's load. Both are timing-dependent and untouched by this branch; `go test ./mgmt -count=1` locally: ok (all mgmt tests pass).
- Cleanup on the test host: the timed-out first run left its jail dir and bind mounts behind (`/var/lib/tw-vm-p<pid>`); unmounted and removed. The second run cleaned up after itself.

## Gaps and notes

- C-scratch on macOS (vz) is not pinned: the guest side (`formatScratch` refusing a non-blank device) is shared code, but no vz test writes a marker and scans the next VM's disk.
- The inspector claim's "whatever mgmt sends" is pinned structurally (the two knobs are hostd `Config` fields, set only by `tw dev`); no test enumerates every mgmt-sent field.
