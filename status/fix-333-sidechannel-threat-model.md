Last update: 2026-11-24 PT. Issue #333: in-process side channels (Spectre, unfrozen clocks, `MAC_KEY` in the workload's process) disclosed in the threat model, the user docs and the ledger; the clock measurement pinned by a test.

## What changed

Documentation plus one pinning test. No production code changes, so no ADR
and no release-note fragment (`make changes-check` passes without one).

- `security/THREAT-MODEL.md`: new §3.1 "Side channels inside a workload's
  process" (which secrets sit in a workload's process: `MAC_KEY`, under
  Landlock `LOCAL_TOKEN`, every facet isolate and the platform wrapper; what
  a leaked `MAC_KEY` yields, all inside the attacker's own workload; B3 is the
  boundary weakened; the measurement; the decisions on rotation, WD-3 and a
  frozen-clock upstream ask). The §5 MAC-key claim now says it holds against
  JavaScript only; B3 points at §3.1; a `Revised:` line at the top.
- `docs/security-model.md`: new "Side channels inside a workload" under
  Isolation tiers (placed before "What the process-tier sandbox confines",
  so it doesn't touch the shared-hosts text #334 is editing), and a Known
  gaps bullet after the sealed-caller one.
- `security/LEDGER.md`: `H-sidechannel-inproc` (Medium, hostile tenant
  code, **disclosed**, #333), claims row `C-clocks` (pinned by
  `gen.TestTenantClocks`), a run-log line.
- `hostd/gen/clocks_test.go` + fixture `hostd/testdata/apps/clocks/`:
  `TestTenantClocks` drives the generated config in the pinned workerd from
  the Worker and from a facet object and asserts what tenant code sees
  today: `Date.now()` and `performance.now()` take more than one value
  during a CPU loop, `SharedArrayBuffer` exists, `node:worker_threads`'
  `Worker` throws. Logs the step size, `Atomics.wait` and shared
  `WebAssembly.Memory`. The comment says what a failure means (a workerd
  bump changed the clocks; re-read §3.1).

## Measurement (workerd 1.20261121.1, macOS arm64, generated config)

Both from the Worker and from a facet object: 200 ms CPU loop, about 3.3 M
iterations; `Date.now()` 201–202 distinct values; `performance.now()` 202
distinct values, min step 1 ms; `SharedArrayBuffer` and `Atomics.wait` are
functions; shared `WebAssembly.Memory` builds; `new Worker` from
`node:worker_threads` throws `ERR_METHOD_NOT_IMPLEMENTED`. Matches the
issue's measurement on 1.20261118.1.

## Commands run

| Command | Result |
|---|---|
| `make deps` | ok (workerd 1.20261121.1 installed) |
| `go test -count=1 -run TestTenantClocks -v ./hostd/gen/` | PASS (Worker, FacetObject) |
| `go test -count=1 ./hostd/gen/` | ok (8.5 s) |
| `make test-docs` | ok |
| `make lint` | ok (gofmt, vet, staticcheck, vulncheck, api-check, compat-check, changes-check, check-secrets) |
| `make test` (locally, after merging `origin/master` at 69c15e4a) | ok (exit 0; 58 packages ok, no FAIL). The mbp offload (`sh ~/.claude/scratch/mbp-test.sh test`) queued behind two other agents' slots, so it was stopped once the local run passed |

Not run: nothing in the table in AGENTS.md applies (no store, placement,
templates, wrangler, upgrade or install changes).

## Decisions recorded (THREAT-MODEL §3.1)

- Option 1 (accept and document) and option 4 (pin the measurement) done.
- Option 2: no scheduled restart or `MAC_KEY` rotation now; moving the MAC
  root out of the process waits on upstream WD-3.
- Option 3: a frozen-clock config switch for self-hosters noted as an
  upstream ask, not implemented; experimental timer flags are #337.
- Cross-process (shared host) side channels are #334; this branch does not
  edit that text.
