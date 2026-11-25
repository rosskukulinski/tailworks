Last update: 2026-11-21 22:10 PT. Linux matrix re-run from v0.3.0 blocked on the test tailnet's policy; pool-node rejoin fixed; harness takes a published release.

# test-113-linux-matrix (issue #113)

## What changed

- **Pool nodes rejoin after a restart** (`hostd/poolnodes.go` `rejoinsWith`,
  one call in `hostd/workload.go` `rejoinObjects`). The ubuntu24-arm64
  "pool node not restored after reboot" failure: `rejoinObjects` only
  restarted objects whose `Class` was `""` or a node class of the app. A
  pool node's `Class` is `_pool` (its real class is the `Name` prefix), so
  it never matched, never went live again, and mgmt kept listing it as
  ready with its old node ID. Now a pool node rejoins when its pool class is
  a node class of the app. Test: `TestRejoinsWithPoolNodes` (fails with
  the old condition: `rejoinsWith(pool-agent) = false, want true`).
- **`make e2e-linux LINUX_RELEASE=vX.Y.Z`**: `scripts/e2e-linux.sh` downloads
  that GitHub release's linux tarballs with `gh release download` into
  `dist/published/<tag>/`, checks them against its `SHA256SUMS`, and runs
  the rows on them; `make release` is skipped. `install.sh` in the VM checks
  the signature (no `--allow-unsigned`).
- **`LINUX_APPLY_POLICY=1`** (`TW_E2E_APPLY_POLICY=1`): the e2e/linux test
  first adds `cliflow.Policy()` to tailnet 1's policy with
  `policy.ApplyWith` (additions only, validated, If-Match; previous copy
  saved next to the credentials), as e2e/mac does. Default 0: read only.
- **`TW_E2E_VM_PREFIX`** (default `twl-`): this Mac's matrix VMs predate the
  rename and are `twdl-<distro>`; the test's guard accepts both prefixes.
- OAuth mode: the harness was already OAuth-only for tailworks (the
  run's own OAuth client via `cliflow.RealOAuthClient`; the API key stays on
  the Mac to create/delete it and clean up). Fixed the comments that still
  said an API key file is copied into the VM.
- Docs: every column of docs/install-testing.md's matrix now says when,
  on which build, with which minting and sandbox it was measured, and that
  no row has been measured from a published release in OAuth mode with the
  sandbox. Fedora is caveated in docs/INSTALL.md and not added to
  `LINUX_DISTROS` (it did not run here).

## The run (2026-11-21, v0.3.0)

- Release: `v0.3.0` is a **draft** on GitHub (tag pushed, signed assets);
  `v0.2.0` is the latest published one. Used v0.3.0's assets (downloaded
  with `gh`, both linux tarballs `OK` against `SHA256SUMS`).
- `make e2e-linux LINUX_RELEASE=v0.3.0 TW_E2E_VM_PREFIX=twdl- LINUX_DISTROS="ubuntu24-arm64 debian12-arm64"`:
  both VMs recreated fresh (template ubuntu-24.04 / debian-12, vz, 4 CPU /
  6 GiB / 30 GiB, no mounts), then both rows **FAIL** in 0.4 s at
  `create the run's OAuth client: 400 requested tags [tag:tw] are invalid or not permitted`.
  Cause: tailnet 1's policy still has only the pre-rename `tag:twd*`
  tagOwners and grants; nothing owns `tag:tw` or `tag:tw-*`. Every
  real-tailnet suite that creates an OAuth client hits this.
- A rerun with `LINUX_APPLY_POLICY=1` (adds the tw-* tagOwners and grants)
  was refused by the agent's permission system as a policy write to a real
  tailnet. **Not run. Needs Ross:** apply the policy additions (or approve
  the run with `LINUX_APPLY_POLICY=1`), then run the matrix below.
- Nothing was created on tailnet 1 (the test stopped before any OAuth
  client, key or node). Policy untouched. A copy of it as found is saved as
  `tailnet1_acl.pre-113-linux-matrix-20261121.hujson` next to the
  credentials.
- Tailnet 1 has no ttest nodes. It has two offline pre-rename devices from
  2026-11-15 (`twd-mgmt`, `twd-host-h-lima-twdl-kvm-u24`), not created by
  this run; left for Ross to decide.
- VMs used: `twdl-ubuntu24-arm64`, `twdl-debian12-arm64` (recreated fresh,
  stopped). No other VM touched.

## To finish #113 once the policy has the tw-* tags

```
make e2e-linux LINUX_RELEASE=v0.3.0 TW_E2E_VM_PREFIX=twdl- LINUX_APPLY_POLICY=1 \
    LINUX_DISTROS="ubuntu24-arm64"          # alone first: it writes the policy
make e2e-linux LINUX_RELEASE=v0.3.0 TW_E2E_VM_PREFIX=twdl- \
    LINUX_DISTROS="debian12-arm64 ubuntu22-arm64 fedora-arm64 ubuntu24-amd64"
```

v0.3.0 does not contain the pool-node fix, so its ubuntu24-arm64 reboot
phase is expected to fail the rejoin count; a release cut after this
branch should pass it.

## Tests

- `go test ./hostd -run 'TestRejoinsWithPoolNodes|TestPoolable|TestIdlePool'`: PASS
  (and FAIL with the old condition).
- `make lint`: PASS. `make changes-check`: ok.
- `make test` (mbp runner, slot2, `-race`): PASS (hostd 301 s, cmd/tw 294 s, mgmt 265 s).
- `make lint` again after merging origin/master: PASS.
- Not run: the matrix itself (blocked above); `e2e-linux-vm`, `e2e-linux-reboot`.
