Last update: 2026-11-21 PT. Issue #115: a minimal set of resource limits, and the availability-isolation scope written down.

# Resource limits (issue #115)

## What's built

- **Process-tier V8 heap caps** (`hostd/limits.go`, `hostd/gen/limits.go`).
  - workerd: the generated config sets `v8Flags = ["--max-old-space-size=N"]`,
    default 1024 MiB per isolate (`tw-hostd -workerd-heap-mib`, -1 = off).
    Verified against `workerd.capnp`: `Config.v8Flags` exists. The schema has
    **no** per-request CPU limit or per-isolate memory limit, so neither is
    promised. Checked by hand with the pinned workerd: past the cap, V8
    aborts the process (`Reached heap limit`), and hostd restarts it like any
    crash.
  - Node tier: `--max-old-space-size=2048` ahead of the app's arguments
    (`tw-hostd -node-heap-mib`), unless `node.args` or `NODE_OPTIONS` already
    set one.
- **mgmt API limits** (`mgmt/ratelimit.go`, wired in `internal/mgmtd` inside
  `TailnetHandler`): a token bucket per WhoIs node (100 rps, bursts of 200;
  hosts 1000 rps, bursts of 2000), and a gate of 256 in-flight requests
  shared by non-host callers. Hosts are outside the gate, so a user flood
  can't delay a heartbeat into a fence. mgmt's own node and the local socket
  are exempt. Over a limit: `429 rate_limited` with `Retry-After` (new
  ErrorCode, `docs/errors.md`). Flags: `tw-mgmt -api-rps`, `-api-host-rps`,
  `-api-inflight` (-1 = off).
- **Docs:** in `docs/limits.md`, a new "Process tier" section, rows in
  "Management API", and "Limits that don't exist" rewritten so each row
  gives why the limit is deliberately not provided. `docs/security-model.md`
  has a new section, "Availability between tenants": a process-tier vs.
  microVM table, and a statement that the process tier gives no
  availability isolation beyond the heap cap.

## Deliberately not provided (see docs/limits.md)

- Per-request CPU limit: workerd has none to configure. `RLIMIT_CPU` is
  lifetime CPU, so it would kill servers.
- Whole-process memory (RLIMIT_AS / cgroup): RLIMIT_AS breaks V8's
  reservations. cgroup v2 `memory.max` needs a delegated subtree (unit
  `Delegate=yes`, and hostd moving itself into a leaf), which is a bigger
  change. macOS enforces neither. A possible follow-up is Linux cgroups
  when tw-hostd runs under systemd with delegation.
- netd per-workload concurrent-request cap: WebSockets make an in-flight
  count a poor bound, and a flood mostly costs the target workload's own
  workerd.
- Node capacity derived from RAM: left to #114.

## Tests

- `TestGeneratedHeapCap` (hostd/gen): real workerd with a 64 MiB cap; a
  Worker allocating ~1 GiB aborts workerd with the heap-limit message.
  Without a cap the same request succeeds.
- `TestProcessTierHeapCaps` (hostd): default caps reach a deployed
  workerd's config and the Node-tier process's argv.
- `TestHeapCapDefaults`, `TestNodeHeapArgs` (hostd, unit).
- `TestAPIRateLimitPerCaller`, `TestAPIInFlightGate`,
  `TestAPIRateLimitBehindTailnetHandler` (mgmt).
- `TestLimitsDocCitations` resolves the new rows.

Commands run: see the PR's Tests section.
