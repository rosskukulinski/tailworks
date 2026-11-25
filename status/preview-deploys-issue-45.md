Last update: 2026-11-15 PT. Preview deploys and `twd promote` (issue #45 item 3), ADR-deploy-preview Proposed.

Continues `status/deploy-history-rollback-issue-45.md` (items 1 and 2,
merged in PR #57). With this branch every item of issue #45 is built.

## What's built

- **Preview deploys.** `POST /v1/deployments` with `preview: true` (or a
  `previewAlias`) and `twd deploy --preview [--preview-alias <alias>]`.
  The bundle runs as the workload `<workload>--preview-<alias>` (default
  alias: the first 8 hex digits of the bundle's SHA-256), next to the
  workload, which is not touched.
  - Its own lease and epoch, record, assignment, node and identity, so it
    never takes or fences the workload's Durable Object lease. Its DO
    storage and KV/R2/D1 stores are its own and start empty.
  - The workload's tags and secrets (released at the preview's own epoch,
    audited with `previewOf`).
  - No Tailscale Service, no public routes, one replica.
  - Bundles that bind queues are refused (`400 preview_unsupported`):
    queues are tenant-wide. So is a preview of a preview or replica.
    A name taken by a non-preview workload is `409 preview_conflict`.
  - A redeploy of the preview by its own name (host drain, `twd rollback`
    of the preview) keeps it a preview.
- **List.** `GET /v1/tenants/{id}/workloads/{workload}/previews` and
  `twd previews <workload> [--json]`.
- **Promote.** `POST /v1/tenants/{id}/workloads/{workload}/promotions`
  (`dryRun` preflights) and `twd promote <workload> <alias>
  [--keep-preview] [--host] [--replace] [--force-takeover]`. It runs
  `Mgmt.Deploy` for the workload with the preview's bundle (lease
  epoch+1, fencing, sticky placement, the workload's replicas, Service and
  public routes; built images reused by digest), then undeploys the
  preview. Audit `kind: promote` with `promotedFrom` and `preview`
  (privileged). Refusals: `404 unknown_preview`, `409 preview_failed`.
- Code: `mgmt/preview.go`, `cmd/twd/preview.go`. Hooks: `mgmt/service.go`
  (two DeployRequest fields and one private field, a 3-line entry call in
  `Deploy`, one `strip` and one `mark` call, the `promote` privileged
  kind), `mgmt/types.go` (`ClassConfig.PreviewOf`), `mgmt/secrets.go`
  (secrets owner lookup), `mgmt/rollback.go` (audit kind and detail),
  `cmd/twd/app.go` (two flags, one body call, one result field, the
  printout), `cmd/twd/main.go` (two commands). No store migration.
- Docs: ADR draft `docs/design/adr/deploy-preview.md`, `docs/errors.md`
  (four codes), `docs/cli-json.md`, `docs/d1.md`, `docs/secrets.md`,
  `schema/cli.schema.json` (`previews`, `promote`, deploy's `preview`),
  `changes/preview-deploys-issue-45.md`.

## Tests

- New: `TestV1PreviewPromote` (lease and deployment of the workload
  untouched, the preview's own lease, same bundle = same preview, no
  Service, secrets inherited, stores not, aliases, redeploy by name, the
  four refusals, dryRun, promote with and without `keepPreview`, audit
  detail), `TestPreviewStrip`, `TestAuthzPreviewIsAdminOnly`.
  `TestAgentEval` now runs `twd deploy --preview-alias try`, `twd previews`
  and `twd promote` through to ready; `TestCLIJSONContract` covers the
  two new commands.
- `make lint`: pass.
- `go test ./mgmt/... -count=1`: pass.
- `make test` (test-go, `-race`): every package passes except two that
  failed once under load and pass when rerun alone, in code this branch
  doesn't touch: `netd TestRejoinRejectedRemints` ("identity_pending:
  object gone node not ready: context deadline exceeded") and `hostd/gen
  TestGeneratedWorkflows/AppCannotApprove` (the same flake PR #57 saw).
  `mgmt` and `cmd/twd` pass; `TestAgentEval` passes (31 s), including the
  preview and promote steps.
- `make test-js`: 32 passed, 0 failed.

## Decisions

- A preview never shares DO state or stores with the workload (stricter
  than Cloudflare, whose previews write to production bindings). Secrets
  are shared, as for replicas and Cloudflare previews.
- Promote is a deploy, like rollback: no pointer flip, so the workload's
  fencing and placement rules hold.

## Gaps / next

- No traffic splitting or canaries (`versions deploy --percentage`).
- No MCP `preview`/`promote` tool.
- Undeploying a workload doesn't stop its previews.
- Previews of bundles with queue bindings.
- No check at promote that the preview's D1 migrations were applied to the
  workload.
