Last update: 2026-11-24 PT. Undeploy then redeploy of the same app works (#364).

# fix-364-undeploy-redeploy

## Root cause

`tw undeploy` kills every object of the workload, its entry node
(namespace `_workload`) included. Object IDs are deterministic
(HMAC of tenant, workload, namespace, name), so the next deploy asked
for the same entry node; `CreateObject` found the killed row and answered
`410 killed`, and `tw deploy` failed with `bootstrap_failed: the workload
node for <app> is killed` until `tw down --purge` forgot every killed
object of the tenant. Two more layers would have kept the names killed
after mgmt released them:

- hostd keeps killed objects as tombstones and `admit` refuses their
  names (#153), so a redeployed app's Durable Objects killed by the
  undeploy stayed refused on that host.
- netd's kill tombstone (10-12 s) closed the old node and removed its
  state dir on a timer. A node of the same ID created during that window
  shared the dir with the still-running old node, and the timer then
  deleted the new node's state (`TestCreateEndsTombstone` fails without
  the fix).

## Fix

- `mgmt/redeploy.go`: `Deploy` (once the lease is held, before the
  deployment is recorded) checks whether the workload has a running
  deployment. If its latest one is stopped (or it has none), it forgets
  the workload's killed and failed objects: device deletes first (in
  parallel), then `DeleteDeadObject` (row + kill revocations), each
  audited as `purge` with reason "workload deployed again after
  undeploy". One call added in `mgmt/service.go`.
- `hostd/unassignedtombstones.go`: `syncAssignments` forgets killed
  objects of workloads no longer assigned to the host (once their kill
  has finished). mgmt stays the authority (410 on create).
- `netd/tombstone_end.go`: `Registry.Create` ends a killed object's
  tombstone (close the old node, remove its dir) before a new object of
  the same ID takes the dir; the timer then does nothing.
- `cmd/tw/workloadnode.go`: a killed workload node's error says
  `tw undeploy <app>`, then deploy again (instead of "tw logs").
- hostd's `killed` admit message, `docs/errors.md` (`killed`,
  `bootstrap_failed`) and `docs/create-admission.md` describe the release.
  The old `killed` entry wrongly said a new request recreates the name.
- `scripts/demo.sh`: removed the `burned` check and the "use another
  DEMO_NAME after --cleanup" messages; `scripts/demo_test.sh` now reruns
  the demo after `--cleanup` under the same names; `docs/demo.md` updated.

## Decision taken (flag for review)

A deploy after an undeploy releases **every** killed name of the
workload, including names killed with `tw kill` before the undeploy, not
only the ones the undeploy killed. While the workload stays deployed a
kill stays permanent, as before. Rationale: undeploy ends the workload;
it gives a documented per-workload way to release a killed name without
`tw down --purge`. The released object's DO storage is not deleted (same
as after `tw down --purge`).

## Tests

- `go test -run 'TestRedeployAfterUndeploy|TestPurgeAfterKill|TestV1DeleteSemantics' ./mgmt/`: PASS
  (`TestRedeployAfterUndeploy` fails without the `mgmt/service.go` call:
  `410 killed ... was killed (undeployed)`).
- `go test -run 'TestUndeployedTombstonesForgotten|TestRT3_H19' ./hostd/`: PASS.
- `go test -run TestCreateEndsTombstone ./netd/`: PASS (fails without the fix:
  the new node's state dir is gone).
- `go test -run 'TestCLIEndToEnd$' ./cmd/tw/`: PASS with the new
  `redeploy_after_undeploy` step; without the mgmt fix it fails with
  `bootstrap_failed: the workload node for agent is killed`.
- `make lint`: PASS.
- `make test` (on the mbp runner, `mbp-test.sh test`): PASS.
- `make test-pg`: PASS.
- `make demo-test` (Headscale in Docker): PASS, including the new run
  after `--cleanup` under the same names. It needed the
  `scripts/demo-headscale.sh` flag fix (`--policy-apply` →
  `--apply-policy --yes`), which #371 also made on master (same line).
- After merging origin/master (#371): `make lint`, `TestRedeployAfterUndeploy`,
  `TestCLIEndToEnd`: PASS.
- Not run: `make e2e-placement` (placement unchanged; the release runs
  after placement, before the deployment is recorded), `e2e-headscale`,
  real-tailnet suites (`e2e-real`, `e2e-mac`).
