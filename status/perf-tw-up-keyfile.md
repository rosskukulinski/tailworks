Last update: 2026-11-23 10:20 PT. `tw up` starts one tsnet node fewer: mgmt joins its own node with a single-use key file.

# perf-tw-up-keyfile (#245, continues `status/perf-245-real.md`)

## What changed

- **mgmt joins its own node.** `tw up` used to join mgmt's node in the CLI,
  close it, and have tw-mgmt open it again: two tsnet starts (three with
  `--separate-host`), each about 1 s of round trips to production control
  (`GET /key`, register, map). Now `tw up` mints the key (as before, after
  the policy preflight) and hands it to mgmt in `<state>/mgmt/join.env`.
  Ross approved this trade-off; it replaces the rule that the key is "used
  in-process and never stored".
  - The file is 0600, written atomically, in mgmt's 0700 state dir (which
    the process sandbox denies to workloads).
  - mgmt (`internal/mgmtd/joinkey.go`, `Options.TSNetAuthKeyOnce`,
    `tw-mgmt -tsnet-authkey-once`) reads the key and deletes the file
    before it joins. A Start that fails also deletes it, and records why
    in `<state>/mgmt/start.err` (redacted, first failure wins). A key file
    group or others can read is refused and deleted.
  - `tw up` deletes the file when it returns, so a key mgmt never read
    doesn't outlive the command. The key expires within 5 minutes anyway.
- **What `tw up` checked on its own join it now checks after mgmt's**
  (`cmd/tw/up_mgmtjoin.go`): the tag, the tailnet (discovered from mgmt's
  node when the OAuth client can't list devices), the funnel attribute. mgmt
  publishes them in `loopback.json` (`nodeID`, `tailnet`, `tags`, `funnel`).
  The supervisor may now start before the tailnet is known; hostd's
  `-mgmt` URL (separate layout) comes from the loopback descriptor.
- **Failures.** If mgmt's join fails, `tw up` reports control's reason
  (`policy_missing_tag_owners` for a tag refusal, else `bootstrap_failed`)
  and stops the supervisor; on a first `tw up --service` it removes the
  service, as when the CLI's join failed before installing it. If mgmt's
  saved node was logged out (deleted in the admin console), `tw up` mints a
  key and starts again once; this replaces the CLI's interactive login.
- The health poll is 20 ms instead of 50 ms.
- Docs: `docs/security-model.md`, `security/THREAT-MODEL.md` B5, ledger
  claim `C-mgmt-joinkey`, `login.go` comment.

## Tried and dropped

- **Minting mgmt's key during the preflight** saved ~140 ms on a first up,
  but it mints before the policy check. `TestCLIOAuth` pins "no key minted
  before the policy is fixed", so it is out.
- **Reading the policy and discovering the tailnet in parallel** measured no
  faster on macOS (api_check 711–766 ms vs 710–754 ms before): reverted.

## Found, not changed (need a decision or are out of scope)

- **WhoIs of mgmt's own IP fails on a cached netmap.** On a restart, mgmt's
  node comes up from tsnet's netmap disk cache in ~0 ms, but the CLI's
  `GET /v1/healthz` (through the loopback, so WhoIs of mgmt's own IP) gets
  `no_identity` until control's first netmap arrives, ~1 s later. A
  self-IP short-circuit in mgmt's WhoIs would save ~1 s on every re-up and
  restart. It is an auth-path change, so not done here.
- **Warm-key revocation on stop is serial.** In one separate-layout run,
  `systemctl --user restart` took 3.7 s because mgmt revoked 6 warm keys at
  ~0.6 s each (`mgmt_ms=3663`). Parallel revocation would bound restart.
- **Separate layout: hostd can't start its node before mgmt's** without a
  new trust flow: its key comes from mgmt (`POST /v1/hosts`, with the
  registration secret). Not changed.
- **Control-side outliers.** A new node's first registration sometimes takes
  4–6 s on production control (`tailnet node … ms=4811`, `5810`); master
  showed the same (an 8.3 s first up on macOS). Medians below exclude
  nothing; outliers are noted.

## Numbers

Hermetic (`TestUpDownBudget`, testcontrol, macOS), `tw up` start → ready:
0.35–0.45 s before, 0.05–0.13 s after; tsnet starts per up 2 → 1 (the test
counts `GET /key` through a proxy and fails on master).

macOS, production control (tailnet 1, scratch state dir, single process,
3 state dirs × 3 ups each, same harness, master built the same day):

| | before (master 75aadcb) | after |
|---|---|---|
| first `tw up` → ready | 3.40, 3.24, 8.34 s | 2.01, 5.21, 5.65 s; earlier runs 1.72–1.77 s (×9) |
| `tw up` again (saved state) | 3.03–3.89 s (median 3.1) | 1.78–2.09 s (median 1.84) |

The after first-up outliers are mgmt's first registration (4.2 and 4.8 s
in tsnet); in 9 further fresh ups on the same code it was 1.0–1.1 s.

Linux, `make e2e-linux LINUX_DISTROS=ubuntu24-arm64 TW_E2E_VM_PREFIX=twdl-`
(fresh Lima VM per run, production control, tailnet 1; before from
`status/perf-245-real.md`, same harness on master):

| | before | after |
|---|---|---|
| single process: `tw up --service` → ready | 3.84 s | 2.55, 3.70, 3.39, 6.95 s |
| separate: `tw up --service` → ready | 5.45, 5.40 s | 4.15, 4.52 s |

Per run (single): 2.55 s = preflight 0.59 + spawn and mgmt's one tsnet
start; 3.70 s had a 1.5 s key mint, 3.39 s a 1.7 s preflight, 6.95 s a
5.8 s first registration (all control/API latency, in the phase timings
and mgmt's `tailnet node … ms=`). mgmt's first tsnet start was 1.07–1.18 s
otherwise, and there is no second one. Restart → healthy and reboot →
healthy are unchanged within noise (1.49–1.97 s single; one 7.7 s with a
6.9 s re-registration; separate 3.0 s, one 7.0 s from the warm-key
revocation above). The first run (2.55 s) was an intermediate build with
the early mint; the rest are the final code.

Every run: 0 policy writes; devices and keys cleaned by the harness; the
macOS runs used `ttest-uptime-*` nodes and an OAuth client that was
deleted at the end, and deleted their mgmt devices. `twdl-ubuntu24-arm64`
is stopped.

## Tests

- `make lint`: PASS.
- `make test` (mbp runner): PASS. (The first run failed on two things this
  branch caused, both fixed: the early mint broke `TestCLIOAuth`, and
  `TestUpMgmtJoinKeyRemoved` raced on testcontrol's `RequireAuthKey`.)
- `make e2e-tw-upgrade`: PASS (`TestTwUpgradeSingleMachine`,
  `TestUpDownBudget`: 1 tsnet start per up, up 0.07–0.10 s).
- `make e2e-upgrade`: PASS.
- `make e2e-linux LINUX_DISTROS=ubuntu24-arm64`: 6 runs, all PASS (4
  single, 2 separate).
- New: `internal/mgmtd` `TestJoinKeyOnce` (deleted after success, after a
  refused join, after a failure before the join; 0644 refused; start.err
  carries control's reason and not the key); `cmd/tw`
  `TestMgmtJoinKeyFileMode` (0600, dir 0700) and `TestUpMgmtJoinKeyRemoved`
  (gone after a failed and a successful `tw up`, supervisor stopped);
  `TestStartCloseBudget` now starts as tw up does (single-use key file).
