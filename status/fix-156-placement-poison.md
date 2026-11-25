Last update: 2026-11-21 PT. H23 (#156): hostile-host review of placement inputs; ingest bounds fixed, plausible lies disclosed, corroboration proposed.

# fix-156-placement-poison

## Review (security/LEDGER.md H23)

Which decisions one hostile host's path reports can swing:

| Decision | Swung by one host's report? |
|---|---|
| Another host's rows ("make h2 look unreachable") | No. `roleGate` binds `/v1/hosts/{id}` to the caller, rows are keyed by host, and liveness is each host's own heartbeat. A report can only make the reporter look better or worse. |
| Tenants it doesn't run | No (`403 forbidden`, `ReportHostPaths`). |
| A running workload (plain redeploy) | No: sticky. |
| A stateful move | Not by itself: `--replace --force-takeover` (live holder), `--force-takeover` (dead holder) or drain `--allow-local-state-loss`, all operator acts, and storage doesn't follow the move. The destination of a confirmed move is chosen by the reports. |
| A new workload of a tenant it runs; dead-holder re-place; `--replace` of a stateless workload; drain destinations | **Yes**, with plausible numbers (disclosed). |
| Out-of-range numbers | Were a win: fixed (below). |

## Fixed

- `rttMs` ≥ ~9.2e15: `int64(rttMs*1000)` overflowed. With `aggregate: sum`
  the total wrapped negative on any arch (on amd64 one value is enough).
- `ageMs` ≥ ~9.2e12: `time.Duration` overflow gave a `measuredAt` ~290 years
  ahead: fresh for ever, never deleted by `pathRetention`, never re-probed.
- `mgmt/placement_bounds.go`: ingest clamps RTT to 1 min and age to 1 h
  (reports and probe replies both go through `storePaths`). Scoring and
  `probeMissing` ignore a stored row with an RTT outside [0, 1 min] or a
  `measuredAt` more than 1 min ahead (rows stored before the fix).
- Tests: `mgmt.TestRT3_H23_PlacementPoison` (WhoIs gate, spec-validated)
  and `mgmt.TestRT3_H23_StoredRowsBounded`. Both fail with the fix removed
  (checked: the 1e300 ms host and the overflowed-age host were chosen; a
  negative-RTT row and a future row won).

## Proposal (needs Ross; not implemented)

One host's plausible lie can't be told from the truth. Options:

1. Confirm smart placement of stateful workloads: when placement picks a
   stateful workload's host by `smart` (not pinned, not capacity), require
   `--host` or an approval (docs/approvals.md) before the lease is taken.
2. Corroborate with an input the host doesn't control, such as the control
   plane's view of the host's and target's DERP home regions and endpoints,
   or a path measured from the target side (a tenant client reporting).
   Don't credit a class better than that view allows.
3. Per-tenant trusted placement hosts: only listed hosts may win on path;
   the others are scored on capacity.

Out of scope here: a host that also runs a target node can drop other
hosts' pings to it, so their honest reports say `unreachable`.

## Tests run

- `make lint`: ok.
- `make test` (mbp runner): every package ok except `mgmt`, which failed
  only on `TestWarmKeyPool`, a data race in test code (`e.logs` read
  without its lock). That race is unrelated to this change; it is filed as
  #222 (flaky-test). Rerun: `go test -race -count=1 ./mgmt/` ok (201 s).
- `make e2e-placement`: ok (`TestSmartPlacementHermetic`,
  `TestR2MoveBetweenHosts`).
- `go test ./mgmt -run TestRT3_H23` with the fix removed: both tests fail
  (shown above).
- `make test-pg`: not run, because the store is unchanged.
