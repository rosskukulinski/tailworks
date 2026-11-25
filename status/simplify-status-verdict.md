Last update: 2026-11-24 PT. Issues #455 and #456: `tw status` leads with the verdict and prints internals only when nonzero; `tw up` / `tw status` report saved unsafe flags every run.

# tw status verdict and saved unsafe flags (issues #455, #456)

Branch `simplify-status-verdict`. Part of the simplify audit (#434).

## Built

- **#455, host `tw status`** (`cmd/tw/status_view.go`): header
  `tw status · healthy|unhealthy · <state dir> · tailnet <tailnet>` (mint
  mode and transport dropped from the header; still in `--json` and
  `tw doctor`). The mgmt line appends mint queue, pending device deletes
  and warm keys in custody only when nonzero; registrations/min dropped
  from the human line. Host line: `N objects`, with a breakdown only when a
  non-ready status is nonzero. The Worker internet line is printed only
  when its mark is `!` or the path is not `direct`.
- **#455, linked `tw status`**: per tenant `N deployments (tw ls)`, plus a
  `!` line for any deployment with an error or a non-active status. The
  image-pin sub-lines moved to `tw ls` (`printImagePins`, under each
  deployment).
- **#455, JSON**: the linked result now has `stateDir`, so it passes
  `$defs/status`. The schema documents `linked`, `whoami`, `hosts`,
  `tenants[].deployments`, `containers`, `hostCapacity`,
  `containerReservations`, `degradedStores`, `unsafeFlags`. Host
  `workloads` is `[]`, never `null` (it was `null` with no deployments,
  which failed the schema). `docs/cli-json.md` explains the two field
  sets; `docs/upgrading.md` no longer claims `versions.skewed` is in
  `tw status --json` (it is mgmt's `GET /v1/status`).
- **#456** (`cmd/tw/up_unsafe.go`): `tw up` (after the host line) and
  `tw status` print `! unsafe` for saved `--insecure-shared-host`,
  `--allow-experimental-flags`, `--local-dev`, and a neutral `› saved` line
  for `--ephemeral`, `--allow-sibling-egress`. `tw status` marks a running
  host whose `InsecureSharedHost` differs from tw.json. `unsafeFlags` on
  `upResult` and the host `statusResult` (not on the linked result).

## Decisions

- `whoami` in the linked JSON is mgmt's raw `GET /v1/whoami`, not the
  `tw whoami` result, so the schema types it as an object with that
  description instead of `$ref: #/$defs/whoami` (which would fail: it
  requires `linked`, `transport`, `can`, …). Changing the field's shape
  would break readers.
- The clear hint says `tw down, then tw up --<flag>=false`: a `tw up` on a
  running supervisor saves the flag but the running host keeps the old
  value until a restart. `--local-dev` can't be cleared (`state_conflict`),
  so its line says "fixed for this state dir".
- `hostd.WorkloadStatus` stays Go-cased (as the issue says).

## Tests

- `go test ./cmd/tw -run 'TestObjectsLabel|TestMgmtExtras|TestUnsafeFlags'`: pass.
- `go test ./cmd/tw -run 'TestCLILink$|TestCLISingleProcess$|TestCLIJSONContract$'`
  (after `make deps`): pass. TestCLILink now checks `tw status --json`
  against `$defs/status` on the host and on the linked machine, the
  verdict header, the `› saved --ephemeral` line and `1 deployment (tw ls)`.
- After merging `origin/master`: `make cli-docs` (no change), `make lint`
  pass (check-changes: needs cli-json, found in the fragment); `make test`:
  every package passed except `hostd` `TestTelemetry/EntryTraces/ObjAlarm`
  (timed out waiting for an alarm record on the loaded machine), which
  passes alone (`go test ./hostd -run 'TestTelemetry$'`); `make test-js` pass.
- `make demo-test`: skipped, no Docker on this machine. `scripts/demo.sh`
  reads `tailnet` and `mintMode` from `tw status --json`, which are
  unchanged.
