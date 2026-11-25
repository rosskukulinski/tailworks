package main

import (
	"strings"
	"testing"
)

func snap(ds ...upgradeDeployment) deploymentsSnapshot {
	s := deploymentsSnapshot{}
	for _, d := range ds {
		s[d.key()] = d
	}
	return s
}

// The issue #63 case: support was failed before the upgrade and fails
// again after it (lease_lost); that must not roll back the upgrade.
func TestGateIgnoresDeploymentsFailedBefore(t *testing.T) {
	t.Parallel()
	before := snap(
		upgradeDeployment{DeploymentID: "dp_app", Workload: "app", Status: "active"},
		upgradeDeployment{DeploymentID: "dp_support", Workload: "support", Status: "failed", Error: "EADDRINUSE"},
		upgradeDeployment{DeploymentID: "dp_old", Workload: "old", Status: "stopped"},
	)
	after := []upgradeDeployment{
		{DeploymentID: "dp_app", Workload: "app", Status: "active"},
		{DeploymentID: "dp_support", Workload: "support", Status: "failed", Error: "lease_lost"},
		{DeploymentID: "dp_old", Workload: "old", Status: "stopped"},
	}
	if why := gateDeployments(before, after); why != "" {
		t.Fatalf("gate = %q, want pass (support was failed before)", why)
	}
	// An active one that hasn't come back still blocks.
	after[0].Status = "pending"
	if why := gateDeployments(before, after); !strings.Contains(why, "workload app is pending") {
		t.Fatalf("gate = %q, want app pending", why)
	}
	// So does a stopped one that now failed (it passed the gate before).
	after[0].Status = "active"
	after[2].Status = "failed"
	if why := gateDeployments(before, after); !strings.Contains(why, "workload old is failed") {
		t.Fatalf("gate = %q, want old failed", why)
	}
}

func TestGateWithoutSnapshotChecksEverything(t *testing.T) {
	t.Parallel()
	after := []upgradeDeployment{{DeploymentID: "dp_support", Workload: "support", Status: "failed", Error: "lease_lost"}}
	if why := gateDeployments(nil, after); !strings.Contains(why, "workload support is failed lease_lost") {
		t.Fatalf("gate = %q", why)
	}
	// A deployment that appeared during the upgrade isn't gated on.
	if why := gateDeployments(snap(), after); why != "" {
		t.Fatalf("gate = %q, want pass for a deployment not in the snapshot", why)
	}
}

func TestNotActiveReporting(t *testing.T) {
	t.Parallel()
	before := snap(
		upgradeDeployment{DeploymentID: "dp_b", Workload: "b", Status: "failed", Error: "boom"},
		upgradeDeployment{DeploymentID: "dp_a", Workload: "a", Status: "pending"},
		upgradeDeployment{DeploymentID: "dp_c", Workload: "c", Status: "active"},
	)
	w := notActiveWarnings(before)
	if len(w) != 2 || !strings.HasPrefix(w[0], "workload a is pending before the upgrade") || !strings.HasPrefix(w[1], "workload b is failed (boom) before the upgrade") {
		t.Fatalf("warnings = %q", w)
	}
	r := wasFailedBefore(before)
	if len(r) != 2 || r[0] != "workload a was pending before the upgrade" || r[1] != "workload b was failed before the upgrade" {
		t.Fatalf("result = %q", r)
	}
	if len(notActiveWarnings(nil)) != 0 || len(wasFailedBefore(nil)) != 0 {
		t.Fatal("nil snapshot reported something")
	}
}
