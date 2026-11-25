---
kind: security
upgrade: [protocol]
---
A workload that is not a replica can no longer be deployed under a
replica's name (red-team H-r2group, #348). Replica *n* of `web` is the
workload `web--r<n>`, and a replica shares its workload's stores, secrets
and R2 objects. tw-hostd tells a replica by its name when it stores R2
object bytes, while tw-mgmt goes by the deployment record. For a separate
workload named `web--r2` the two disagreed: its bucket's index was its
own, but its bytes were stored with `web`'s. Nothing could read or delete
another workload's objects through this, and it is now ruled out: a deploy
of such a name (n ≥ 2), or a preview alias that makes one
(`--preview-alias pr7--r2`), is refused with `409 replica_conflict`, at
the deploy and at its dry run. Replicas themselves deploy as before.

The API is unchanged except that `replica_conflict` has this new cause.

Operator action: none for most installs. If you run a workload of its own
whose name ends in `--r<n>` (`tw ls`), it keeps running and keeps
deploying while it is live, but rename it when you can: deploy it under a
new name, move its data, and undeploy the old one. Once undeployed, the
old name can't be deployed again.
