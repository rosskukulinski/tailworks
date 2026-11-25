package cliflow

import (
	"fmt"
	"path/filepath"
	"time"
)

// redeployAfterUndeploy is `tw undeploy agent && tw deploy agent` on the
// same host (issue #364): the second deploy must reach a ready workload
// node. It was refused with `bootstrap_failed: the workload node for agent
// is killed` until `tw down --purge`. Deploying right after the undeploy
// also exercises a new node of the same ID while the old one's kill
// tombstone is still up. The new node IDs are tracked for the leftover check.
func (e *Env) redeployAfterUndeploy() {
	t, res := e.T, e.Res
	t.Helper()
	var un struct {
		Result struct {
			Killed []string `json:"killed"`
		} `json:"result"`
	}
	e.MustJSON(&un, "undeploy", "agent", "--reason", "e2e: redeploy after undeploy")
	if len(un.Result.Killed) < 1 {
		t.Fatalf("undeploy agent: %+v", un)
	}
	var dep struct {
		Status   string `json:"status"`
		Endpoint struct {
			MagicDNS string `json:"magicDNS"`
			Status   string `json:"status"`
		} `json:"endpoint"`
	}
	d := e.MustJSON(&dep, "deploy", filepath.Join(e.WorkDir, "agent"))
	if dep.Status != "active" || dep.Endpoint.Status != "ready" || dep.Endpoint.MagicDNS == "" {
		t.Fatalf("redeploy after undeploy: %+v", dep)
	}
	e.TrackObjectNodes()
	res.pass("redeploy_after_undeploy", fmt.Sprintf("agent %s ready again after undeploy (%d objects killed) in %v", dep.Endpoint.MagicDNS, len(un.Result.Killed), d.Round(time.Millisecond)))
}
