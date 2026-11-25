Last update: 2026-11-21 22:30 PT. Per-node memory and registration rate measured; capacity-nodes default derived from RAM (issue #114).

# bench-114-node-density

## What's built

- `netd/bench_test.go`: `BenchmarkRegistryNodes`, sub-benchmarks
  `peers=scoped|mesh/N=10|100|500`. The registry runs in a child process
  (the test binary re-executed as `TestBenchNodeChild`, which skips
  otherwise), so its RSS (read with `ps`) excludes testcontrol, DERP, STUN
  and the load generator. Reports base, idle and load-peak RSS per node, Go
  heap per node, goroutines, join time, req/s, p50/p99, and checks the peer
  count an object sees (1 scoped, N mesh).
- `netd/bench_scope_test.go`: scoped visibility on testcontrol (which has
  no visibility rules) through `AltMapStream`: each object's netmap holds
  only the caller.
- `make bench` (`BENCH_NODES` picks sizes).
- `e2e/regrate` + `make e2e-regrate-real`: up to 50 fresh ephemeral
  `ttest-bench-<run>-*` nodes on tailnet 1, one reusable key (or
  `TW_REGRATE_MINT=1`: a single-use key per object), JoinParallel
  `TW_REGRATE_PAR`. Cleans up devices and keys by ID.
- `hostd/capacitynodes.go`: `DefaultCapacityNodes(totalMiB)`: 25% of RAM at
  8 MiB per node, within [50, 1000], 500 when RAM is unknown. hostd.New uses
  it when `CapacityNodes` is 0; `tw-hostd -capacity-nodes` defaults to 0 and
  `hostdrun` no longer sets 1000. Tests: `TestDefaultCapacityNodes`,
  `TestCapacityNodesDefaultFromRAM`, `TestCapacityVector` updated.
- Docs: limits.md "Per-node memory (measured)" and "Registration rate
  (measured)" under Hosts, capacity row updated; INSTALL.md "Sizing";
  ADR-006 amended with the alternatives; RFC as-built row, §9 note, §4.9
  parking/gcAfter options (open decision), §13 Q1 partly answered.

## Results (2026-11-21)

Per node, idle / under load (32 concurrent keep-alive callers):

| Host | Peers | N=10 | N=100 | N=500 |
|---|---|---|---|---|
| Linux arm64, Lima vz VM `twdl-bench-u24` (8 vCPU, 16 GiB) | scoped | 3.4 / 8.8 MB | 2.3 / 4.6 | 2.7 / 3.9 |
| Linux amd64, `twd-amd64` KVM guest (2 vCPU) | scoped | 2.8 / 5.8 | 2.1 / 3.0 | 2.5 / 2.5 (CPU-bound, 36 req/s) |
| macOS arm64 (M5 Pro, shared with other agents) | scoped | 4.4 / 9.8 | 2.3 / 5.3 | 2.4 / 4.1 |
| Linux arm64 | mesh | 4.1 / 9.9 | 4.7 / 7.3 | child panicked: `ipnlocal: watchdog timeout` at ~10 GB during joins |
| Linux amd64 | mesh | 3.5 / 5.9 | 6.1 / 6.2 | not run (6 GiB guest) |
| macOS arm64 | mesh | 5.7 / 9.9 | 12.2 / 12.2 | 32.6 / 33.1 (16 GB; 4 req/s) |

Zero request errors in every completed run. Hermetic joins on Linux take
~3 s per node at N=10 (vs 0.5 s on macOS); real-tailnet joins are below.

Registration on tailnet 1 (50 nodes each run, all deleted, keys deleted):

| Keys | JoinParallel | 50 ready in | per min | join p50/max | register RTT p50/max |
|---|---|---|---|---|---|
| reusable | 16 | 5.8 s | 513 | 1.50/1.71 s | 0.43/0.46 s |
| single-use per object | 16 | 6.3 s | 479 | 1.38/1.53 s | 0.43/0.45 s |
| reusable | 50 | 2.6 s | 1,148 | 2.00/2.58 s | 0.43/0.44 s |

No throttling seen at 50; the rate is JoinParallel / join time.

## Commands run

- `make bench`-equivalent: `go test -run '^$' -bench RegistryNodes -benchtime=1x ./netd/` on macOS (mesh run before the peers dimension existed, then `peers=scoped/`).
- Linux: `GOOS=linux GOARCH=arm64|amd64 go test -c ./netd/`, run in `twdl-bench-u24` (all) and on `twd-amd64` (`N=(10|100)` mesh, all scoped).
- `make e2e-regrate-real` three times (default; `TW_REGRATE_MINT=1`; `TW_REGRATE_PAR=50`): PASS, cleanup tracked 50 / deleted 50 / left 0 each.
- `make lint`: ok.
- `make test` (mbp runner, slot2, after merging origin/master): exit 0, 108 Go packages ok, JS 42/42.

## Open

- **Decision for Ross:** parking / `gcAfter` (options in RFC §4.9 as-built note).
- RFC §13 Q1: sustained rates beyond 50 nodes and several hosts at once were not measured.
- Lima VM `twdl-bench-u24` is stopped and left in place.
