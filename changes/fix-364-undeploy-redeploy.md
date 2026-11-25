---
kind: fix
upgrade: []
---
`tw undeploy <app>` followed by `tw deploy` of the same app works again
(#364). The redeploy used to fail with `bootstrap_failed: the workload
node for <app> is killed` until `tw down --purge`, which purges every
killed object of the tenant on the machine. Undeploy kills the
workload's objects, and the next deploy reused the same node identity.

Now a deploy of a workload that has no running deployment (its latest one
was undeployed) first forgets that workload's killed objects. The host
forgets their tombstones once the workload is no longer assigned to it.
Every name the workload had can then be created again, including names
killed with `tw kill`. Each release is audited as `purge`. While a
workload stays deployed (redeploys, rollbacks, moves), a killed name
stays killed, as before. If a deploy finds its workload node killed, the
error now says to undeploy the app and deploy it again.
`scripts/demo.sh --cleanup` no longer needs a new `DEMO_NAME` for the
next run.
