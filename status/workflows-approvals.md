Last update: 2026-11-15 10:30 PT. Workflows on workerd's built-in engine and approvals by tailnet identity (issue #24): built, tested hermetically, PR open.

Design: [ADR-040](../docs/design/adr/040-workflows-approvals.md) (Proposed; numbered at the merge to master).
User doc: [docs/approvals.md](../docs/approvals.md). Example: `twd init <dir> --template refund-approval`.

## What's built

- **workerd 1.20261114.1** (root `package.json`, `cmd/twd/doctor.go` pin, `docs/COMPAT.md`,
  `docs/INSTALL.md`): the first release containing workerd#8331 (merged 2026-11-13 15:54 UTC;
  `v1.20261113.1` does not contain it, `v1.20261114.1` does, checked with the GitHub compare API).
  What else the bump changed: nothing in twd's tests (see results). twd already ran workerd with
  `--experimental`; master's platform stores (ADR-platform-stores) use twd's own Store DO, not
  workerd's bundled KV/Queues workers, so the flag and config plumbing are shared and nothing
  else overlapped.
- **Bundle** (`hostd/bundle/workflows.go`): `wrangler.jsonc` `workflows` (`name`, `binding`,
  `class_name`, `limits.steps`, `default_retention`). `script_name` (another Worker) and
  `schedules` are refused at deploy, as are name clashes with DO / store bindings and DO classes.
  `twd deploy` no longer warns that `workflows` is unused (`cmd/twd/wranglercfg.go`).
- **Config generation** (`hostd/gen/workflows.go`, hooks in `gen.go`): an engine service
  `workflows` (`bundled = "/workerd/workflows.js"`, instance storage on the workload's
  `do-storage` disk, `globalOutbound` deny), the tenant's `workflowsEngine`, the tenant's
  `env.<binding>` as workerd's `cloudflare-internal:workflows-api` wrapped binding over the
  platform's `WorkflowGate`, and `WF_<name>` engine bindings in the platform.
- **Platform** (`hostd/gen/js/platform.js`): `WorkflowGate` (forwards everything; refuses
  app-sent `approval`/`approval-*` events with `approval_requires_identity`; strips `approver`
  from other events); facet env gets a `WorkflowGate` stub per binding; `cf.twd` kind `approval`
  delivers a hostd-authorised event; TailnetApi `approval` op (wait reports, instance checked to
  exist).
- **Runtime** (`js/packages/tailnet/src/workflows.ts`, built into `hostd/gen/js/twd-rt.js`):
  `wrapWorkflowClass` (guarded env; reports approval waits as durable steps
  `__twd_approval_requested:<step>` / `__twd_approval_timeout:<step>`), the Workflow client for
  facets, and the `AgentWorkflow` abort fix (below).
- **hostd** (`hostd/approvals.go`): the persisted list (`<data>/approvals.json`),
  `/_twd/approvals[/<workflow>/<instance>]` on workload and object nodes (GET list / one, POST
  decide; JSON only; public refused; grants from netd's caller capMap), push API `GET
  /v1/approvals` and `POST /v1/approvals/decide` for mgmt, delivery, and audit kinds
  `approval_requested|decision|denied|timeout`.
- **Grants** (`internal/approvecap`): `twd.dev/cap/approve` values narrow by `workloads`,
  `workflows`, `steps`, `decisions`, `limits` (numeric params, fail closed); union of grants.
- **mgmt** (`mgmt/approvals.go`, `api/openapi.yaml`): `GET /v1/tenants/{id}/approvals`,
  `POST /v1/tenants/{id}/workloads/{w}/approvals/{workflow}/{instance}`; new `x-twd-role`
  part `approver` (a tailnet caller holding the cap toward mgmt is admitted to these routes
  only); WhoIs now carries the cap's values; errors `cap_missing`, `approval_not_pending` in the
  ErrorCode enum; audit kinds in `AuditKind` and `PrivilegedKinds`.
- **CLI** (`cmd/twd/approve.go`): `twd approvals [workload] [--all]`, `twd approve <workload>
  <instance> [--reject] [--comment] [--workflow] [--step]`; `twd audit` describes the new kinds.
  An approver who is not an admin passes `--tenant tn_…` (tenant listing is admin-only).
- **Admin UI** (`mgmt/ui/approvals.go`, `templates/approvals.html`): an Approvals page with
  Approve / Reject forms; approver-only users get that page and nothing else.
- **Template** `refund-approval` (`js/packages/create-twd/templates/refund-approval`): an Agent
  starts an `AgentWorkflow` per refund, `waitForApproval`, the Workflow refuses the requester,
  and a same-origin approvals page at `/`.
- **Docs**: `docs/approvals.md`, the ADR draft, COMPAT rows (`workflows` ✅, new `approvals` ✅,
  Agents SDK details: `AgentWorkflow` ✅, `waitForApproval` ✅, `approveWorkflow` from app code
  🚫), README rows, `docs/errors.md`, `docs/ADMIN-UI.md`, `docs/wrangler.md`, `templates/README.md`.

## Tests

New, hermetic:

| test | what |
|---|---|
| `hostd/gen:TestGeneratedWorkflows` | stock workerd + generated config, from the Worker, a facet object and a plain-DO object: steps with a retry, sleep, `sleepUntil`, pause/resume/terminate/restart, `createBatch`; app-sent `approval`/`approval-*` refused; reports reach hostd once; platform delivery |
| `hostd/gen:TestParseWorkflows` | wrangler parsing and refusals |
| `hostd:TestApprovalRegistry`, `TestAuthorizeApproval`, `TestServeApprovalsRefusals` | idempotent reports, persistence across a restart, timeout; the grant rules; endpoint refusals (public, no identity, not JSON, preflight, no grant, nothing pending), refusals audited |
| `mgmt:TestApprovals` | roles (no grant → `not_admin`, approver sees only what its grants cover, admin sees all but needs a grant to decide), identity forwarded, host errors relayed, local socket `no_identity`, the UI page and form |
| `cmd/twd:TestApproveCLI`, `TestDescribeApproval` | `twd approvals` / `twd approve` from a linked machine through a real mgmt to a fake host |
| `e2e/sdk:TestWorkflowApprovals` | real `agents` 0.33.0 `AgentWorkflow` + `waitForApproval` in both object modes on twd-hostd over testcontrol: retry from the failed step, no grant / outside limits refused and still waiting, `approveWorkflow()` refused, **hostd restart while waiting**, approve with a forged `approver` in the body and metadata (the Workflow sees the WireGuard identity), second decision 409, reject, the mgmt push path, audit records |
| `e2e/sdk:TestRefundApprovalTemplate` | the template with two tailnet identities: approved by another node, over-limit refused, the requester's own approval refused by the Workflow |

Commands and results (macOS arm64, after merging origin/master `6591638`: D1, Queues, lease locks, mgmt MCP; ADR numbered 040):

- `make lint`: PASS (api-check, compat-check, gofmt, vet of every tag set, check-secrets)
- `make test`: PASS (`go test -race` of netd, internal, cmd, mgmt, hostd, security, guest; `npm test`)
- `make test-sdk`: PASS: TestWorkflowApprovals, TestRefundApprovalTemplate, TestCloudflareAgents, TestOAuthProviderKV, TestAgentTemplate, TestVercelAI

## Findings

- **`AgentWorkflow` × workerd's engine.** The engine ends an in-memory `run()` that waits longer
  than a minute by throwing `WorkflowAbortedError` through it (it runs again later).
  `AgentWorkflow` reports every thrown error to its Agent, so `onWorkflowError` fired for every
  approval wait. twd's wrapper marks that abort as already reported. Upstream would fix this in
  `agents` (ignore `WorkflowAbortedError`) or the engine (not throw through user code).
- **The step is an RPC stub.** Any property reads as a callable, and `fn.call(...)` is a remote
  call to a method named `call`. The runtime uses a `WeakSet` and `Reflect.apply`.
- An event sent before the wait starts is queued by the engine and taken by the next wait of
  that type; twd only delivers to waits it has listed, so this does not open a hole.
- `netd:TestEgressWaitsForNewPeerPath` failed on this Mac right after the workerd bump, before
  any other change (2 of 3 runs; the test involves no workerd), and passed in the final `make
  test`. A timing flake; not investigated here.

## Gaps and next steps

- `subscribe()` and `deleteBatch` are forwarded but not tested by twd.
- `approval_requested` / `approval_timeout` are reported by the runtime inside the workload
  (platform-checked); decisions and refusals are hostd's own. See the ADR.
- No notification integrations (by scope); escalation is the Workflow's.
- The admin UI and `twd approve` need a grant toward `tag:twd-mgmt`; the app page and
  `/_twd/approvals` need it toward the workload's tag. One grant line can name both.
- Not run here: the real-tailnet suites (`e2e-mac`, `e2e-real`) and the microVM tier with a
  Workflow (the engine's storage is the same `do-storage` disk, so it should behave as objects do).
