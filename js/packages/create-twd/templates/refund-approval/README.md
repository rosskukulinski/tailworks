# refund-approval

An Agents SDK agent starts a durable **Cloudflare Workflow** per refund,
and the Workflow waits for a person to approve it. On twd an approval is a
decision by a **tailnet identity**: twd checks the approver's
`twd.dev/cap/approve` grant before the Workflow sees it, and app code
can't approve its own workflows. The Workflow adds its own rule: whoever
asked for the refund can't approve it.

## Run it

```sh
twd deploy
curl -X POST 'http://<workload node>/agents/refund-agent/alice/refund?order=o1&amount=42'
#   {"id":"<instance>",…}
twd approvals                                     # what's waiting
twd approve <app> <instance>                      # as someone with the grant; <app> is the workload name
curl http://<workload node>/agents/refund-agent/alice/refunds
```

Approvers can also open the workload's page (`/`) in a browser on the
tailnet, or use the admin console.

## What to change

- `src/index.ts` `RefundWorkflow.run`: your steps (`step.do`), the
  approval timeout (7 days here; nothing is refunded when it runs out) and
  your own rules on `approval.approver`.
- The policy: who may approve what. A grant of `twd.dev/cap/approve` to
  this workload's tag, with limits on the Workflow's params, e.g. support
  up to 100 and finance any amount.
- `wrangler.jsonc` `workflows`: the Workflow (`refund`); with more than
  one, pick it with `twd approve … --workflow <name>`. The `approve` hint
  that `/refund` returns names the template's workload, `refunds`; change
  it to your app's name.

More: [approvals.md](https://github.com/rosskukulinski/twd/blob/master/docs/approvals.md).
