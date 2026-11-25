Last update: 2026-11-24 PT. tw up tuning flags (#457) and the workerd/sandbox preflight (#480).

# up-flags-preflight

Issues #457 and #480 from the interface simplification audit (#434).

## What changed

- **#457, dead tuning flags.** `--heartbeat`, `--kill-tombstone`,
  `--device-delete-delay` and `--push-port` are deprecated no-ops
  (`cmd/tw/up_tuning.go`): they still parse, so scripts and saved command
  lines keep working, and tw up prints a note when one is given. They are
  hidden from --help with #550's mechanism (`also accepted, deprecated and
  ignored: …`), to be removed in a later release. The timing values are the
  constants `upHeartbeat` (1s), `upKillTombstone` (12s) and
  `upDeviceDeleteDelay` (8s), written into tw.json only when its field is
  empty; values an older tw saved are still used. `pushPort` is still read
  (separate layout) but no longer written. The cross-flag check and the
  `keep()` lines are gone; the headscale e2e no longer passes the flags.
- **#457, fast identity in the single layout.** `Config.identitySingle`
  (`cmd/tw/up_identity.go`) applies the saved `--warm-keys`,
  `--pool-nodes` and `--port-mapping` to `mgmtd.Options` and
  `hostdrun.Options` (unset = the daemons' defaults, 0 = off), and
  `superviseSingle` calls `netd.DisablePortMapping()` before `mgmtd.Start`
  when port mapping is off. The flag defaults are `mgmtd.DefaultWarmKeys`
  and `hostdrun.DefaultPoolNodes`.
- **#480, preflight.** `upPreflight` (`cmd/tw/up_preflight.go`) runs after
  `findWorkerd` and before mgmt's key is minted: `workerd --version` must
  run (else `workerd_broken`, with doctor's fix text; new entry in
  docs/errors.md), and on Linux without Landlock the "NO PROCESS SANDBOX"
  warning is printed (warn only). Doctor's workerd and sandbox checks now
  share `workerdRuns` and `noLandlock` with it.

## Tests

- `go test ./cmd/tw ./docs -count=1`: ok. New: `TestIdentitySingle`,
  `TestRemovedUpFlags`, `TestUpPreflightWorkerd`; `TestUpKeepsSavedSettings`
  updated.
- `make cli-docs`: docs/cli.md regenerated (again after merging
  origin/master with #550: the four flags moved to the hidden footer).
- `make lint`: ok.
- `make test` (load average ~100): failures only in TestCLIOAuth (cmd/tw),
  TestWorkloadReplicas and TestAPIRateLimitBehindTailnetHandler (mgmt),
  TestR2Streaming, TestMultiTenant and TestWorkersKV (hostd). Each passes
  when run alone (`go test ./<pkg> -run '^<Test>$'`); mgmt and hostd are
  not touched by this branch.
- `make e2e-tw-upgrade`: PASS (TestTwUpgradeSingleMachine, TestUpDownBudget).
- Not run: `make e2e-headscale` (its tw up drops the two removed flags; the
  nightly runs it).
