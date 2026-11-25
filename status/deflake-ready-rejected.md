Last update: 2026-11-22 PT. Deflaked netd.TestReadyNodeRejectedLater (a test race, not a netd one), and fixed a stale H25 line in THREAT-MODEL §6.

## Flake

On master, CI run 45064906900 failed with this:
`rejected_test.go:210: netd: bootstrap_failed: node rejected by control after it was ready`.
The test took 2.40 s, which is one join plus the 2 s grace. So it failed at
the moment the rejection landed.

Root cause: the test's `OnRejected` sent on its channel and only then
called `reg.Create`. The test goroutine wakes on that send and reads
`reg.Get("l")`. If it ran before `Create`, it got the old object, which had
just failed with `errNodeRejected`, so `Ready` returned bootstrap_failed at
once. netd isn't involved: `rejectReady` calls `OnRejected`
synchronously, and hostd's `onNodeRejected` restarts the node inside that
call. The fix: the callback re-creates the object first and signals after.
The test also now fails loudly if `reg.Get` still returns the old object.
`TestRejoinFromNetmapCacheRejectedRemints` gets the same reorder. It was
already tolerant, because it polls for a reminted Ready object.

## Counts

All runs are `-race`, under load (10 busy loops plus 2 loops of other netd
`-race` tests), via `/tmp/tw200/stress-rl.sh`.

- Unfixed test, as is: 0/100, 0/100 with GOMAXPROCS=2, and 0/200 with 36
  busy loops. The window is a few instructions wide, and I couldn't hit it
  on this Mac.
- Unfixed test with a 1 ms sleep inserted between the send and `Create`
  (this only widens the existing window): **50/50 failed**, with CI's exact
  message.
- Fixed test with the same 1 ms sleep, placed after the send: 50/50 pass.
- Fixed test, as is: **100/100 pass**.
- `go test -race -count=10 -run '^(TestRejoinFromNetmapCacheRejectedRemints|TestRejectedAfterKillStaysKilled)$' ./netd/`: ok.

## Also

`security/THREAT-MODEL.md` §6 no longer lists warm keys (H25, #158) as
open. #231 fixed them, and the ledger row was already marked fixed. §6 now
also states the residual risk: while mgmt runs, a custody read still
yields the live pool.
