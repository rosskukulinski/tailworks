# ADR-040: Workflows on workerd's built-in engine; approvals are decisions by tailnet identities

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `workflows`,
`tw approvals`, `tw approve`, [docs/approvals.md](../../approvals.md); results
in `status/workflows-approvals.md`). Implements the
"Workflows engine for `AgentWorkflow`" item of ADR-022.

**Context.**

- Cloudflare Workflows and the Agents SDK's `AgentWorkflow` /
  `waitForApproval` were ❌ on tailworks: no binding, no engine.
- workerd 1.20261114.1 is the first release with a built-in Workflows engine
  (workerd#8331): a bundled Worker, `/workerd/workflows.js`, run with
  `--experimental` (which tailworks already passes). Each instance is a
  SQLite-backed Durable Object. A `run()` that restarts runs from the start,
  with completed steps returning their stored results; a wait longer than a
  minute ends the in-memory run, and the instance's alarm (or an arriving
  event) runs it again. Steps, retries, sleeps, events, pause/resume,
  terminate, restart and batches are supported.
- The Agents SDK's `waitForApproval(step, opts)` is
  `step.waitForEvent(stepName, {type: "approval"})` and reads the payload's
  `approved`, `reason` and `metadata`. `approveWorkflow()` is an ordinary
  `sendEvent` from the Agent.
- On Cloudflare any code holding the binding can send that event, so "who
  approved" is whatever the app writes into the payload. Hosted agent
  platforms offer "pause a run until a person approves", usually through a
  link or a token. On tailworks every caller already has a WireGuard-verified
  identity and tailnet grants (`getCaller`, ADR-006, ADR-010).

**Decision.**

1. **Engine hosting.**
   - tailworks uses workerd's engine unmodified, configured as workerd's own
     `wrangler.json` mapping does: an engine service (`workflows`,
     `bundled = "/workerd/workflows.js"`), the tenant worker's
     `workflowsEngine` naming its `Engine` class, and each Workflow's
     instance namespace `miniflare-workflows-<name>` on the tenant worker.
   - The engine's instance storage is the workload's `do-storage` disk.
     Instances therefore live where the workload's objects live: on the
     host holding the workload's namespace lease, fenced with it (ADR-011,
     ADR-028), and inside the microVM's encrypted volume on that tier
     (ADR-024). A hostd restart or a redeploy keeps them; a lease move
     moves them with the workload's storage, as for any object.
   - **An instance gets no tailnet node.** It is an engine-internal Durable
     Object like a `"node": false` class: nothing addresses it over the
     tailnet, and a node per instance would mint keys for state machines
     that never take a call. The Workflow class runs in the tenant service,
     so its `fetch()` leaves as the workload node, and it reaches objects
     with the guarded env (existing objects only, no create ticket).
   - The engine worker's `globalOutbound` is the platform's deny worker.

2. **The binding goes through the platform.**
   - The tenant's `env.<binding>` is workerd's own Workflow API
     (`cloudflare-internal:workflows-api`), but its inner fetcher is the
     trusted platform's `WorkflowGate`, not the engine. In facet-mode
     objects (a Dynamic Worker) the runtime builds the same API over a
     `WorkflowGate` stub.
   - `WorkflowGate` forwards every call to the engine, with two rules:
     an event of type `approval` or `approval-*` from app code is refused
     (`approval_requires_identity`), and any `approver` key is removed from
     every other event's payload (and its `metadata`).
   - So a Workflow can trust `approver`: only tailworks sets it. The Agents SDK's
     `approveWorkflow()` / `rejectWorkflow()` are refused on tailworks by design.

3. **The approval surface.** A wait for an `approval`/`approval-*` event is
   an approval. Tailworks' runtime wraps each Workflow class's `step.waitForEvent`
   and reports the wait (workflow, instance, step, type, timeout, the
   instance's numeric params) to hostd through the platform, as a durable
   step, so a `run()` that runs again does not report twice. hostd keeps
   the list (`<data>/approvals.json`, so it survives a restart). A decision
   reaches hostd in one of two ways, and hostd alone delivers it:
   - **On the workload's (or an object's) node:** `/_tw/approvals[/<workflow>/<instance>]`
     is intercepted by hostd before the request reaches workerd. The
     approver is netd's verified caller (user, node, node ID, tags); the
     grants are that caller's `github.com/rosskukulinski/tailworks/cap/approve` toward the node. A
     `POST` must be `application/json` (a cross-site page can't send one
     without a preflight, which tailworks never answers), public requests are
     refused, and a same-origin page the app serves can use it.
   - **Through mgmt:** `tw approve` / `tw approvals` and the admin UI call
     `GET /v1/tenants/{id}/approvals` and
     `POST /v1/tenants/{id}/workloads/{w}/approvals/{workflow}/{instance}`.
     mgmt vouches for its WhoIs of the caller and sends the caller's
     `github.com/rosskukulinski/tailworks/cap/approve` grants toward `tag:tw-mgmt` to the lease
     holder's push API (`POST /v1/approvals/decide`, admitted only from
     mgmt's node). The operator console and the local socket have no
     personal identity and are refused (`no_identity`).
   - hostd then sends the event through the platform (`cf.tw` kind
     `approval`, which only hostd can set) with the payload
     `{approved, approver, comment, reason?, metadata: {…, approver, comment}, decidedAt}`.
     `waitForApproval` returns `metadata`, so the approver is in both.

4. **Authorisation.** `github.com/rosskukulinski/tailworks/cap/approve` is an app capability. A grant
   value may narrow it by `workloads`, `workflows`, `steps`, `decisions`
   and numeric `limits` on the instance's params; grants are a union, and
   a limit the instance has no number for does not match (fail closed).
   Being a tailworks admin is not a grant. The rules are one package
   (`internal/approvecap`), used by hostd to enforce and by mgmt to filter
   what an approver is shown. The Workflow may add its own rules on the
   verified identity (the template refuses the requester).

5. **Audit.** Host-chain records: `approval_requested`, `approval_decision`
   (with the approver and comment), `approval_denied` (a refused decision)
   and `approval_timeout`. Decisions and refusals are hostd's own;
   requests and timeouts are reported by the runtime inside the workload
   (platform-checked: one of the app's Workflows, an existing instance), so
   their timing is the workload's word. All are privileged kinds that
   tenant code cannot submit.

6. **Notification, timeouts.** `tw approvals` and the admin UI list what
   waits, with age and params. No built-in email or chat: the Workflow
   notifies from a step. A `waitForEvent` timeout rejects (nothing was
   approved; the default timeout is 24 hours), recorded as
   `approval_timeout`; escalation is the Workflow's (catch and wait again).

7. **One tailworks fix to the SDK interplay.** The engine ends a long wait by
   throwing `WorkflowAbortedError` through `run()`; `AgentWorkflow` reports
   any thrown error to its Agent (`onWorkflowError`). Tailworks' wrapper marks
   that abort as already reported, so the Agent hears only real failures.

**Consequences.**

- Workflows, `AgentWorkflow` and `waitForApproval` work in both object
  modes, on both tiers' storage; the pinned workerd moves to 1.20261114.1.
- An approval on tailworks cannot come from a link, a token or the app's own
  code; it names a tailnet identity whose grant allowed it.
- Apps that approve from their own code (`approveWorkflow()`) must move
  that decision to `tw approve`, the admin UI or `/_tw/approvals`.
- ADR-012 (one host per namespace) is unchanged: a workload's instances
  live on its lease holder. workerd's cluster mode, which replicates engine
  instances, is not used.
- Not done: `script_name` and `schedules` (refused at deploy); notification
  integrations; a decision path for identities without a grant.
