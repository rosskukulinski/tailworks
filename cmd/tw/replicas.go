package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// waitReplicas waits until every replica of a deploy (tailworks.jsonc
// "replicas" > 1) other than the first is active with a ready workload
// node. The first is the deployment cmdDeploy already waits for.
func waitReplicas(ctx context.Context, mc *mgmtapi.Client, dr mgmt.DeployResponse, since time.Time, wait time.Duration) error {
	for _, rp := range dr.Replicas {
		if rp.DeploymentID == dr.DeploymentID {
			continue
		}
		for {
			var rv mgmt.DeploymentView
			if err := mc.Do(ctx, "GET", "/v1/deployments/"+rp.DeploymentID, nil, &rv); err != nil {
				return err
			}
			if rv.Status == "failed" {
				return fail("deploy_failed", "the host could not start replica "+rp.Workload+" on "+rp.Host, strings.Split(rv.Error, "\n")...).withFix("tw logs " + rp.Workload)
			}
			if rv.Status == "active" && rv.Endpoint.Status == "ready" {
				break
			}
			if time.Since(since) > wait {
				return fail("identity_pending", fmt.Sprintf("replica %s on %s not ready after %v (status %s, node %s)", rp.Workload, rp.Host, wait, rv.Status, rv.Endpoint.Status)).withFix("tw status")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}
