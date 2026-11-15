# Workflows and approvals by tailnet identity

twd runs [Cloudflare Workflows](https://developers.cloudflare.com/workflows/)
(`workflows` in `wrangler.jsonc`, `WorkflowEntrypoint`, the Agents SDK's
`AgentWorkflow`) on workerd's built-in engine. A Workflow can pause until a
person approves it. On twd **an approval is a decision made by a specific
tailnet identity**: twd checks the approver's tailnet grant before it
delivers the decision, the Workflow sees who decided (verified over
WireGuard), and the audit log records it. There is no approval link to
forward and no token to leak.

The design is [ADR-040](design/adr/040-workflows-approvals.md).
The template `twd init refunds --template refund-approval` is a working
example.

## Workflows

Declare them as on Cloudflare:

```jsonc
// wrangler.jsonc
"workflows": [{ "name": "refund", "binding": "REFUND_WORKFLOW", "class_name": "RefundWorkflow" }]
```

`env.REFUND_WORKFLOW` works in the Worker and in Durable Objects (both
object modes): `create`, `createBatch`, `get`, and on an instance `status`,
`pause`, `resume`, `terminate`, `restart`, `sendEvent`. Inside `run()`,
`step.do` (with retries, timeouts and backoff), `step.sleep`,
`step.sleepUntil` and `step.waitForEvent` behave as on Cloudflare.

- Each instance is a SQLite-backed Durable Object in the workload's
  Durable Object storage, on the host holding the workload's lease. It has
  no tailnet node of its own; a Workflow's outbound `fetch()` leaves as the
  workload node.
- Instances survive a hostd restart and a redeploy. After a restart, a
  `run()` runs again from the start: completed steps return their stored
  results without running again, and waits keep their deadlines. A failed
  step retries from that step.
- Not supported: `script_name` (another Worker's Workflow; call that
  workload over the tailnet) and `schedules` (use `triggers.crons` and
  create the instance in `scheduled()`). Both are refused at deploy.

## Waiting for an approval

Any wait for an event of type `approval` or `approval-*` is an approval.
The Agents SDK's `waitForApproval` uses type `approval`:

```ts
export class RefundWorkflow extends AgentWorkflow<RefundAgent, { order: string; amount: number }> {
  async run(event, step) {
    const approval = await this.waitForApproval(step, { timeout: "7 days" });
    // approval.approver = { user, node, nodeId, tags, via }: who decided, from WireGuard
    // approval.comment  = what they wrote
    await step.do("refund", async () => { /* ... */ });
  }
}
```

With plain `step.waitForEvent("ok?", { type: "approval" })`, the event's
`payload` is `{ approved, approver, comment, reason?, metadata, decidedAt }`.

When the wait starts, twd lists it (`twd approvals`, the admin UI,
`GET /_twd/approvals`) and records `approval_requested` in the audit log.

**Only twd delivers an approval.** App code cannot send an event of type
`approval` or `approval-*` (the Agents SDK's `approveWorkflow()` and
`rejectWorkflow()` throw `approval_requires_identity`), and twd removes any
`approver` field from events the app does send. So a Workflow can trust
`approver`: it is always twd's, never something a request or the app's own
code made up.

**Timeouts reject.** When a wait times out (the default timeout is 24 hours;
set your own), nothing was approved: `waitForEvent` throws, the instance
errors unless you catch it, and twd records `approval_timeout`. To escalate,
catch the timeout and wait again (for example for `approval-manager`), or
notify someone from a step.

## Deciding

Three ways, all with the decider's own tailnet identity:

| From | How | Identity and grant checked |
|---|---|---|
| your app's own page on the workload's (or an object's) node | `POST /_twd/approvals/<workflow>/<instance>`, JSON `{"decision": "approve" \| "reject", "comment": "…"}`; `GET /_twd/approvals` lists what you may decide | the request's WireGuard caller; its `twd.dev/cap/approve` grants toward that node |
| a terminal on a `twd link`ed machine | `twd approvals [workload]`, `twd approve <workload> <instance> [--reject] [--comment …]` | mgmt's WhoIs of your machine; your `twd.dev/cap/approve` grants toward `tag:twd-mgmt` |
| the admin UI | **approvals** page, Approve / Reject | the same as `twd approve` |

`/_twd/approvals` is answered by twd, never by your code. A `POST` must be
`Content-Type: application/json`: another website can't send that from a
browser on your tailnet without a CORS preflight, which twd never grants.
Public (Funnel) requests are always refused. A same-origin `fetch()` from a
page your Worker serves works (see the template).

On the twd host itself (the operator console, the local socket) there is no
personal identity, so `twd approve` is refused there (`no_identity`).
Being a twd admin does not let anyone approve: the grant does.

## Who may decide: grants

`twd.dev/cap/approve` is an app capability, granted like any other. Each
grant value may narrow it; a missing field does not narrow, and `"*"` in a
list matches anything:

| field | narrows to |
|---|---|
| `workloads` | these workloads |
| `workflows` | these Workflow names |
| `steps` | these wait step names (`wait-for-approval` for `waitForApproval`) |
| `decisions` | `"approve"`, `"reject"` |
| `limits` | `{"<param>": <max>}`: the instance's numeric param must be ≤ max. An instance without that param does not match |

Grants are a union: any one matching grant allows the decision. For
example, anyone in support may approve refunds up to 100, finance any
amount, and the on-call bot may only reject:

```jsonc
"grants": [
  // the app's page and /_twd/approvals on the workload's node
  {"src": ["group:support"], "dst": ["tag:twd-wl-refunds"], "ip": ["tcp:80"],
   "app": {"twd.dev/cap/approve": [{"workflows": ["refund"], "limits": {"amount": 100}}]}},
  {"src": ["group:finance"], "dst": ["tag:twd-wl-refunds"], "ip": ["tcp:80"],
   "app": {"twd.dev/cap/approve": [{"workflows": ["refund"]}]}},
  // twd approve and the admin UI (through mgmt)
  {"src": ["group:finance"], "dst": ["tag:twd-mgmt"], "ip": ["tcp:443", "tcp:80"],
   "app": {"twd.dev/cap/approve": [{"workloads": ["refunds"], "workflows": ["refund"]}]}},
  {"src": ["tag:oncall-bot"], "dst": ["tag:twd-wl-refunds"], "ip": ["tcp:80"],
   "app": {"twd.dev/cap/approve": [{"decisions": ["reject"]}]}},
],
```

A refused decision is `403 cap_missing`, naming what was missing and the
grant to add (`snippet`); the instance keeps waiting.

The Workflow can add its own rules on top, on twd's verified identity. The
template refuses an approval by the person who asked for the refund:

```ts
if (approval.approver.nodeId === event.payload.requester) {
  throw new NonRetryableError("the person who asked for a refund cannot approve it");
}
```

## Audit

Every step is a record on the host's audit chain (`twd audit`, the admin
UI):

| kind | when | detail |
|---|---|---|
| `approval_requested` | the wait starts | workload, workflow, instance, step, the instance's numeric params |
| `approval_decision` | an approval or rejection is delivered | the above, `decision`, `comment`, `approver` (user, node, nodeId, tags, via) |
| `approval_denied` | a decision is refused (`cap_missing`) | the above, `decision`, `code`, `approver` |
| `approval_timeout` | the wait timed out (rejected) | the above |

`approval_requested` and `approval_timeout` are reported by twd's runtime
inside the workload (the instance is checked to exist); decisions and
refusals are made and recorded by hostd itself.

## Noticing a waiting approval

`twd approvals` lists what is waiting, oldest first, with its age and
params; the admin UI's approvals page shows the same. Neither needs a
tenant ID: both default to the tenants on your own tailnet, which
`GET /v1/whoami` reports (`tenants`, with your `approveGrants`). Pass
`--tenant` (or open `/ui/approvals?tenant=<tenant ID>`) only when your
tailnet has several.

### Notifications: a webhook per tenant

A twd admin can have mgmt tell people as soon as a Workflow starts
waiting ([ADR-approval-notifications](design/adr/approval-notifications.md)):

```sh
twd notify set --webhook -        # paste the URL: it is a secret, shown again only as its origin
twd notify test                   # send a test event now
twd notify                        # show (origin, header names, events)
twd notify rm
```

mgmt then POSTs JSON to the URL for every `approval_requested`, usually
within seconds of the wait starting:

```json
{"event": "approval_requested", "tenantID": "tn_…", "workload": "refunds", "workflow": "refund",
 "instance": "…", "step": "wait-for-approval", "facts": {"amount": 120},
 "link": "https://twd-mgmt.<tailnet>.ts.net/ui/approvals?tenant=tn_…#<instance>",
 "cli": "twd approve refunds <instance> --workflow refund",
 "text": "Approval needed: refunds/refund instance … (amount=120). Decide: https://…",
 "sentAt": "2026-11-15T10:00:00Z"}
```

- **Slack, Mattermost, Google Chat:** an incoming-webhook URL works as is;
  they show `text`, and the link opens the request's row in the admin UI.
  Opening the link grants nothing: the approver still decides as their own
  tailnet identity, with their own grant.
- **Your own notifier** (email, paging, routing per app): point the
  webhook at it and read the JSON. A tailnet name (`http://notifier/hook`),
  a `*.ts.net` name or a `100.x` address is dialed over mgmt's tailnet
  node; anything else over the twd host's network. Loopback and link-local
  addresses are refused. Add a credential with
  `--header authorization` (read from stdin, never shown again).
- Each request has the header `Twd-Event: approval_requested`. Delivery is
  tried 3 times; a final failure is in the audit log
  (`notification_failed`), and the approval stays listed.

### Notifying from the Workflow itself

To notify per app, or someone specific, do it from a step before you wait:
a `fetch()` to a notifier on your tailnet (Workers reach tailnet peers
only). The approvals page link works from there too:

```ts
await step.do("notify", () => fetch("http://chat-bot/notify", {
  method: "POST",
  body: JSON.stringify({
    text: `Refund of ${event.payload.amount} needs approval`,
    link: `https://twd-mgmt.<tailnet>.ts.net/ui/approvals#${encodeURIComponent(event.instanceId)}`,
  }),
}));
const approval = await this.waitForApproval(step, { timeout: "2 days" });
```

For email, have the notifier (or a mail service's HTTP API reachable as a
tailnet peer) send it; Workers on twd don't reach the public internet.
