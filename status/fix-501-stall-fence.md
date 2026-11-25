# hostd: fence a host that resumes from a stall (#501)

Found by the data-plane chaos loop (`e2e/dataplane`, F3, Linux): a host
SIGSTOPped past its lease and resumed after a `--force-takeover` made DO
egress calls seconds after the new copy's first.

- `hostd/stallfence.go`: `stallFenced(w)` refuses a stateful (fence=true)
  workload's ingress (`ingressHandler`) and egress (`holdEgress`, so the
  HTTP proxy, CONNECT/DialEgress and the Node tier) when the last
  successful heartbeat was sent more than `FenceAfter` ago, or the host is
  self-fenced. Error: `upstream_unavailable` (503).
- `hostd/host.go` `tick`: `lastHB` is the heartbeat's *send* time, so an
  answer that waited out a stall doesn't reopen the gate. The heartbeat's
  revoke is applied (inside `heartbeat`) before `lastHB` moves.
- `nightly.yml`: e2e-dataplane no longer `allow_failure`.
- Not done: a synchronous fence in `loop()` on a long tick gap. The gate
  covers every path out of the host; DO storage writes stay local.

## Tests


- `go test ./hostd -run 'TestStallFence|TestHoldEgress|TestCronDue|TestMM_F9|TestFence'`: pass. Both
  `TestStallFence*` fail without the fix.
- `make lint`, `make test` (macOS): pass.
- `make e2e-locks`: pass (TestLocksFailover 79 s).
- Linux arm64 (Lima): `TW_DP_SCENARIO=fencing TW_DP_FAULTS=pause-takeover,pause-hostd-takeover
  TW_DP_SEED=20261125 TW_DP_DURATION=5m` e2e/dataplane: pass. 6 stalled-host takeovers,
  0 stale answers, 0 stale side effects (the CI run that found #501 had 2).
