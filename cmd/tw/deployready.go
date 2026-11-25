package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// readyWait is the one place deploy, promote and rollback wait for a
// deployment to come up (#525). Each command used to carry its own copy of
// the loop, and the copies drifted: only deploy knew the crashloop state, so
// a promote or rollback whose workerd kept exiting sat out --wait and then
// reported identity_pending with no cause.
type readyWait struct {
	mc       *mgmtapi.Client
	ev       *events
	id       string        // the deployment
	workload string        // for the `tw logs` hint and the timeout message
	what     string        // how the failure messages name it: "app", "the promoted app"
	since    time.Time     // when the command started: --wait and timings count from here
	wait     time.Duration // --wait
}

// run polls mgmt until the primary is active with a ready node and a
// MagicDNS name, or until the deployment fails, crashloops, loses its node,
// or wait is spent. It returns the last view and how long after since mgmt
// first reported active (0 if it never did), which deploy records as its
// "active" timing.
func (w readyWait) run(ctx context.Context) (mgmt.DeploymentView, time.Duration, error) {
	var v mgmt.DeploymentView
	var activeAt time.Duration
	last := ""
	logs := "tw logs " + w.workload
	for {
		if err := w.mc.Do(ctx, "GET", "/v1/deployments/"+w.id, nil, &v); err != nil {
			return v, activeAt, err
		}
		if st := v.Status + "/" + v.Endpoint.Status; st != last {
			w.ev.emit("status", map[string]any{"deploymentID": w.id, "status": v.Status, "node": v.Endpoint.Status})
			last = st
		}
		detail := strings.Split(v.Error, "\n")
		switch {
		case v.Status == "failed":
			return v, activeAt, fail("deploy_failed", "the host could not start "+w.what, detail...).withFix(logs)
		case v.Status == "crashloop": // it served, then kept exiting (hostd/restart.go)
			return v, activeAt, fail("deploy_crashloop", w.what+" started, then its workerd kept exiting; the host keeps restarting it with backoff", detail...).withFix(logs)
		case v.Endpoint.Status == "failed" || v.Endpoint.Status == "killed":
			return v, activeAt, workloadNodeFailed(w.workload, v.Endpoint.Status) // workloadnode.go
		}
		if v.Status == "active" {
			if activeAt == 0 {
				activeAt = time.Since(w.since)
			}
			if v.Endpoint.Status == "ready" && v.Endpoint.MagicDNS != "" {
				return v, activeAt, nil
			}
		}
		if time.Since(w.since) > w.wait {
			return v, activeAt, fail("identity_pending", fmt.Sprintf("%s not ready after %v (status %s, node %s)", w.workload, w.wait, v.Status, v.Endpoint.Status)).withFix("tw status", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
