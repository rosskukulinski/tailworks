---
name: tw-teardown
description: Use when stopping or removing something on tw - undeploy a workload, kill a Durable Object instance or agent and its sub-agents, or stop tailworks itself - and to know which of these commands ask for confirmation.
---

# Stop and remove things on tailworks

These commands act immediately and can't be undone. **Run one only after
the user has agreed to that specific action** (name the workload or
object you're about to stop). An earlier "yes" to something else doesn't
count.

| Command | What it does | Asks first? |
|---|---|---|
| `tw undeploy <workload>` | stops the workload: kills all its objects, releases its host | no |
| `tw kill <object>` | cuts the object (Durable Object instance or workload node) and every object it spawned off the tailnet in under a second; their nodes are deleted (`--cascade=false`: only the object; the reaper kills its sub-agents within seconds) | no |
| `tw secret rm <NAME>` | deletes a secret (the app loses it on the next deploy) | no |
| `tw down` | stops tailworks on this machine (`--purge` also kills every object) | no |
| `tw tenant rm`, `tw host rm` | remove a tenant or a host (operator commands) | yes, unless `--yes` |
| `tw d1 rm`, `tw kv namespace rm`, `tw r2 bucket rm` | delete a database, a KV namespace or an R2 bucket, with all its data | yes, unless `--yes` |
| `tw queues rm`, `tw queues purge` | delete a queue with its messages, or just its messages | yes, unless `--yes` |
| `tw up` | starts tailworks | yes, unless `--yes` |
| `tw agent setup`, `tw agent remove` | write or delete these skill files | yes, unless `--yes` |

`--yes` only skips a confirmation prompt. Without a terminal (as you run
commands) a command that asks first refuses with `confirmation_required`
and does nothing. Tell the user what will be lost (or run `--dry-run` where
there is one and show them), and add `--yes` only after they agree to that
action.

## Undeploy a workload

<!-- tw-eval: teardown -->
```sh
tw undeploy hello-mcp --reason "done testing" --json   # {"workload", "deploymentID", "result": {"killed": [...]}}
```

`--reason` is recorded in the audit trail; give one. Undeploying an
already stopped workload fails with `already_stopped`.

## Kill one object

```sh
tw ls --json                                    # find the objectID
tw kill <object-id> --reason "<why>" --json
tw audit <object-id> --json                     # confirms who killed it and when
```

A killed object stays killed: calls to its name fail with
`object_killed`. Redeploying the workload doesn't bring it back; a new
object name does.

## Afterwards

`tw ls --all --json` also lists killed objects and stopped deployments.
