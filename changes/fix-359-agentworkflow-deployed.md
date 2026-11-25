---
kind: fix
upgrade: []
---
An Agents SDK `AgentWorkflow` started with `runWorkflow` now runs on a
deployed host when its Agent's class has no node of its own (#359). Before,
it stayed queued: nothing reached the Agent, while the same app worked under
`tw dev`.

A Durable Object class missing from `tailnet.objects` (for example, a
`tailworks.jsonc` with only `tailnet.tags`) gets no node on a deployed host,
because mgmt declares only the classes `tailnet.objects` lists. tw-hostd
still asked mgmt for a node for every such object, on every call to the
object, and refilled a node pool for the class each time. mgmt refused both
(`pool node not created ... declares namespace "_pool"`, logged once a
second). Since the object was never one tw-hostd knew, a Workflow, which
has no create ticket, was refused it as a public request would be. tw-hostd
now gives these classes no node and asks mgmt for nothing. They stay in
facet mode, so their storage is unchanged, and their `fetch()` and
`connect()` leave as the workload node. A Workflow may now reach objects of
any class with no node, including `"node": false` classes. For classes
with their own node it still reaches existing objects only. mgmt's refusal
of a pool node now names the class it was for.
