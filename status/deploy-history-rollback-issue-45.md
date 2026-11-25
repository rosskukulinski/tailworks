Last update: 2026-11-15 PT. Deployment history, bundle retention and `twd rollback` (issue #45 items 1–2), ADR-deploy-rollback Proposed.

## What's built

- **History.** `GET /v1/tenants/{id}/workloads/{workload}/deployments`
  (newest first, paged) and `twd deployments <workload> [--json]`. Each
  record: id, created, deployedBy, bundle SHA-256, status, hostStatus,
  error, host, activeAt, bundleRetained, rollbackOf.
- **Retention.** twd-mgmt `-bundle-retention` (default 10, `-1` = all).
  After every deploy, the workload's bundles beyond the newest N are set to
  NULL; the row and its hash stay. Migration
  `mgmt/migrations/202611152330_deploy_history.sql` (expand only; SQLite and
  Postgres).
- **Rollback.** `POST /v1/tenants/{id}/workloads/{workload}/rollbacks`
  (`dryRun` preflights) and `twd rollback <workload> [<deployment>]`.
  Runs `Mgmt.Deploy` with the stored bundle (lease epoch+1, fencing, sticky
  placement, `--replace/--host/--force-takeover` as deploy). Default target:
  the newest earlier deployment that ran, isn't failed, is retained and has
  a different bundle. Refusals: `409 no_rollback_target`, `409
  bundle_pruned`, `404 not_found`. Audit `kind: rollback` with `rollbackOf`.
- Code: `mgmt/deployhistory.go`, `mgmt/rollback.go`,
  `mgmt/sqlstore_deployhistory.go`, `cmd/twd/deployments.go`,
  `cmd/twd/rollback.go`. Hooks in `mgmt/service.go`: one DeployRequest
  field, the audit kind line, one `afterDeploy` call, one
  `noteDeploymentStatus` call, one Config field.
- Docs: ADR draft `docs/design/adr/deploy-rollback.md`, `docs/errors.md`
  (two codes), `docs/cli-json.md`, `docs/d1.md` (forward-only),
  `docs/secrets.md` (not versioned), `schema/cli.schema.json`.

## Tests

- `make lint`: pass.
- `make test` (test-go): every package passes except two that failed once
  under load and pass when rerun alone, in code this branch doesn't touch:
  `netd TestEgressWaitsForNewPeerPath` and `hostd/gen
  TestGeneratedWorkflows/AppCannotApprove`. `mgmt` and `cmd/twd` pass,
  including `TestAgentEval` (a real `twd rollback` to the first deployment,
  through to ready) and `TestCLIJSONContract`.
- `make test-js`: 26 passed, 0 failed.
- Postgres 17 (the `scripts/test-pg.sh` commands, in a separately named
  container): store pass (`Lease|Audit|Store|Migrat`) and API pass
  (`TWD_TEST_API_POSTGRES=1`, `API|Kill|WIF|Tailnet|V1|TestWarmKey`) both
  pass, including `TestStoreDeploymentHistory`, `TestV1DeployHistoryRollback`
  and `TestAuthzRollbackIsAdminOnly`.
- Finding: on master, the Postgres store helper dropped tables but kept
  `schema_migrations`, so numbered migrations never re-ran
  (`TestStoreHostPaths` failed on Postgres for `hosts.version`). Fixed in
  `mgmt/store_test.go`: the helper now recreates the `public` schema.

## Gaps / next

- Preview deploys and promote (issue #45 item 3): separate ADR.
- No MCP `rollback` tool.
- Retention is mgmt-wide, not per tenant or workload.
