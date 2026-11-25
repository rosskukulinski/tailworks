# fix-567-stale-pool-nodes

#567 (found by the e2e/dataplane containers loop, #423): a workload's pool
nodes stayed `ready` in mgmt, with their devices on the tailnet, after the
workload's lease moved to another host.

Fix: `mgmt/poolstale.go`. After a deploy places the workload on a host, mgmt
kills the workload's live `_pool` objects whose deployment ran on another
host (only the lease holder warms pool nodes, so those can never be handed
out again). The kill deletes the device. One call in `Deploy`, in a
goroutine so a cut-off old host can't slow the deploy.

Regression test: `mgmt/poolstale_test.go` `TestStalePoolNodesKilledOnMove`
(fails without the fix: the old host's pool node stays ready).
