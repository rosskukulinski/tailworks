package main

// workloadNodeFailed is the deploy-wait error when the workload's entry node
// is "failed" or "killed". A killed workload node stays killed while the
// workload is deployed; undeploying it and deploying again releases the
// name (mgmt/redeploy.go, issue #364), without purging the machine.
func workloadNodeFailed(workload, status string) *cliErr {
	e := fail("bootstrap_failed", "the workload node for "+workload+" is "+status)
	if status == "killed" {
		return e.withFix("tw undeploy " + workload + ", then deploy it again (that releases the workload's killed names)")
	}
	return e.withFix("tw logs --component hostd")
}
