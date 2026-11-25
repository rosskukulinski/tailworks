Last update: 2026-11-24 20:20 PT. #432 fixed: hostd reaps workerd/Node process groups a crashed predecessor left running.

# hostd: orphaned workerds after a crash (#432)

Found by the data-plane chaos loop (`e2e/dataplane`, F2): 8 of 8
`crash-hostd` faults left the old workerd running.

- `hostd/childreap.go`: `childLedger` in `<DataDir>/children.json`. `add`
  runs after every workerd or Node spawn and `remove` once its group is
  reaped. `openChildLedger` (called from `New`, before anything is spawned)
  SIGKILLs and waits for every listed group whose leader still has the
  recorded start time. Start times come from `/proc/<pid>/stat` field 22 on
  Linux (`childreap_linux.go`) and `kern.proc.pid` on darwin
  (`childreap_darwin.go`).
- Not done: Linux `Pdeathsig`. It fires when the forking *thread* exits,
  and procsb uses `runtime.LockOSThread`, so it could kill healthy
  workerds. The ledger covers restarts, and systemd covers the
  no-restart case on Linux. The microVM tier's VMMs are out of scope.
- `e2e/dataplane`: a new invariant, D7 (no workerd outlives its hostd's
  restart), gates the `crash-hostd` fault.
- `e2e/queues`: the "crash of hostd and workerd" step now kills the
  workerds too.

## Tests

- `TestChildLedgerReapsSurvivors`, `TestChildLedgerTracksLive`: pass.
- See the PR for `make lint test`, `e2e-dataplane` (crash-hostd) and `e2e-queues`.
