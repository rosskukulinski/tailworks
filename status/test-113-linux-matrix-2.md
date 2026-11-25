Last update: 2026-11-22 08:55 PT. Linux matrix re-measured (OAuth only, sandbox on) on v0.3.0 and master; Fedora added to the default rows. Continues status/test-113-linux-matrix.md.

# test-113-linux-matrix-2 (issue #113)

## Test tailnet 1 policy (kept as the new baseline)

- Saved a copy of the previous policy next to the credentials
  (`tailnet1_acl.pre-113-tw-tags-20261122.hujson`).
- Changed it with a mechanical rename, validated against the policy API.
  Ross applied the file himself, because the agent's permission system
  refused the write.
  - `tag:twd*` became `tag:tw*` in every tagOwner, grant, autoApprover and
    funnel nodeAttr.
  - `svc:twd-mgmt` became `svc:tw-mgmt`.
  - The admin cap `twd.dev/cap/admin` became
    `github.com/rosskukulinski/tailworks/cap/admin`.
  - Nothing else changed (`tag:ttest*`, `tag:agent-*`, the `svc:ttest-*`
    services and the example caps).
- Before the write, `policy.Missing` against the new file found no gaps for
  `baseSnippet()` (what `tw up` checks), `cliflow.Policy()`,
  `policy.InfraSnippet(7474)` or the admin grants. The tags and services
  used by the svc, multimgmt, tssvc, real, coldid, oauth and public suites
  are present too.

## Cleanup on the test tailnet

- Deleted the two offline pre-rename devices (`twd-mgmt` and the host
  device of the old kvm-u24 VM), both last seen 2026-11-15: HTTP 200 each.
- The three leftover `#116` warm-pool keys: the delete was refused by the
  agent's permission system, but all three had already expired
  (`invalid: true`) at 06:46 UTC.
- The runs below on builds before #231 left their warm-pool keys valid
  after `tw down`. tailworks didn't revoke them then, and the harness didn't
  check for them. There were about 36 keys, all with 30-minute expiries,
  the last at 16:58 UTC. They expire on their own; deleting them was refused
  as above.
- No devices left on the tailnet after the runs.
- Every VM used (`twdl-{ubuntu24,debian12,ubuntu22,fedora}-arm64`,
  `twdl-ubuntu24-amd64`, `twdl-{debian11,alpine}-arm64`) was recreated
  fresh per row and is stopped.

## Matrix runs (2026-11-22, OAuth only, Landlock on)

| Row | v0.3.0 (draft release, `LINUX_RELEASE=v0.3.0`) | master `0.3.0-45cd9203` (local, after #228) |
|---|---|---|
| ubuntu24-arm64 (+reboot, +ephemeral) | **FAIL**: hostd log after reboot: 3 rejoins (want 4). The idle pool node didn't rejoin, the bug #228 fixed. Every check before it passed. | 1st **FAIL**: harness race, `tw ls` in the `--ephemeral` phase ran before mgmt was back after `systemctl restart` (fixed here: wait for healthy first). 2nd **PASS**: reboot rejoined 4 of 4. |
| debian12-arm64 | **PASS** | **PASS** |
| ubuntu22-arm64 | **PASS** | **PASS** |
| fedora-arm64 | **PASS** | **PASS** |
| ubuntu24-amd64 (qemu) | **PASS** | **PASS** |
| debian11-arm64 (refusal) | **PASS** | **PASS** |
| alpine-arm64 (refusal) | **PASS** | **PASS** |

Also ran ubuntu24-arm64 on master `0.3.0-cf0e5c16` (after #231 and #232)
with the new warm-key check: **PASS**. mgmt minted 42 warm keys and none
was valid after `tw down`. `tw down` took 3.6 s and the restart command
1.1 s.

Numbers are in docs/install-testing.md. Every passing row had 0 devices
left, 0 tw keys valid after down, and a clean VM.

Found: `tw up`, `tw down`, service restart and reboot → healthy are about
10x slower than on 2026-11-08. Filed #245 (lead: mgmt exits 5 s after
hostd on every stop, `mgmtd.Runtime.Close`'s 5 s shutdown grace).

## What changed

- `e2e/linux`: the `--ephemeral` phase waits for `tw status` healthy after
  `systemctl --user restart` before polling `tw ls` (which `t.Fatal`s).
- `e2e/linux`: new cleanup check. The keyIDs in mgmt's `warm key minted`
  log lines are read before the state dir is removed. Any key still valid
  after `tw down` fails the run and is deleted
  (`warm_keys_valid_after_down`).
- `LINUX_DISTROS` and `scripts/e2e-linux.sh`'s default rows now include
  `fedora-arm64`, and docs/INSTALL.md's Fedora caveat is gone.
- docs/install-testing.md: the matrix is re-measured, with each build and
  its date, and says v0.3.0 was a draft release.

## Tests

- 2026-11-22 matrix runs: as in the table above.
- `make lint`: PASS (after merging origin/master). `make changes-check`: ok.
- `make test` (mbp runner, `-race`): PASS.
- Not run: `e2e-linux-vm`, `e2e-linux-reboot` (KVM VM, not in scope).
