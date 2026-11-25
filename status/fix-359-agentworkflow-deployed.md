# fix-359-agentworkflow-deployed

Issue #359: an Agents SDK `AgentWorkflow` started with `runWorkflow` never
ran on a deployed host. It ran under `tw dev`.

## Root cause

- `bundle.Parse` marks every `durable_objects` class as a node class unless
  `tailworks.jsonc` says `"node": false`. mgmt declares only the classes
  `tailnet.objects` lists. e2e/compat's overlay (`tailnet.tags` only) left
  `TaskAgent` a node class in tw-hostd and undeclared at mgmt.
- Every call to a `TaskAgent` object (the facet's `Host_` wrapper ensures
  before each call) asked mgmt to create it. mgmt refused with
  `unknown_namespace`, and the runtime treats that as non-fatal. Each refused
  create still counted as pool demand, so tw-hostd also asked for a `_pool`
  node, which mgmt refused with a message naming `"_pool"` instead of the
  class. That is the once-a-second WARN in the issue.
- tw-hostd never knew the object, so `admit` from the Workflow (no ticket)
  went through `admitPublic` and got `public_create_denied`.
  `AgentWorkflow._initAgent` → `getAgentByName` threw before the user's
  `run()`, so there was no progress report and no console line.
- fakemgmt (`tw dev`, the hermetic suites) declares every class, so the
  object was created there and the Workflow reached it.

## Fix

- `hostd/nodeless.go` (new): `nodesFromMgmt` clears `Node` on classes the
  assignment doesn't declare. These classes keep their egress mode, so their
  storage stays put. `ensure` refuses them, so mgmt gets no create and no
  pool. A facet's `ensure` answers without a node. Facet egress and
  `connect()` leave as the workload node. `admitWorkflow` lets a Workflow
  reach any class with no node, including `"node": false` classes (needed
  once the runtime's 60 s admit cache expires). Anything else still gets the
  `admitPublic` answer.
- Runtime: `guardTailnetEnv` (used only by Workflows) sends
  `workflow: true`, and `platform.js` forwards it.
- `hostd/objects.go`: only a create that mgmt accepts counts as pool demand.
- `mgmt/service.go`: the `_pool` refusal names the class.
- `e2e/compat/upstream.go`: the deployed XFAIL for `ag-workflows` is gone.

## Tests

See the PR body for the commands and results.
