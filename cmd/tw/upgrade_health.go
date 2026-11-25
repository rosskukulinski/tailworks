package main

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The homelab upgrade's deployment gate (issue #63). It used to require
// every deployment to be active after the restart, so a deployment that
// had already failed before the upgrade (and fails again after it) rolled
// back a good upgrade. Now tw upgrade snapshots the deployments before it
// stops tailworks and gates only on the ones that were active then; the others
// are reported, and `--plan` warns about them up front.

// upgradeDeployment is the part of a deployment the gate looks at.
type upgradeDeployment struct {
	DeploymentID string `json:"deploymentID"`
	Workload     string `json:"workload"`
	Status       string `json:"status"`
	Error        string `json:"error"`
}

// deploymentsSnapshot is the deployments' statuses at one point.
type deploymentsSnapshot map[string]upgradeDeployment

func (d upgradeDeployment) key() string {
	if d.DeploymentID != "" {
		return d.DeploymentID
	}
	return "workload:" + d.Workload
}

// deploymentOK is the set of statuses that pass the gate.
func deploymentOK(status string) bool {
	switch status {
	case "active", "stopped", "superseded":
		return true
	}
	return false
}

func listDeployments(s *state) ([]upgradeDeployment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var page struct {
		Items []upgradeDeployment `json:"items"`
	}
	if err := s.mgmt().Do(ctx, "GET", "/v1/deployments?limit=200", nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// snapshotDeployments records the deployments before the upgrade (nil if
// mgmt doesn't answer: the gate then checks every deployment).
func snapshotDeployments(s *state) deploymentsSnapshot {
	ds, err := listDeployments(s)
	if err != nil {
		return nil
	}
	snap := deploymentsSnapshot{}
	for _, d := range ds {
		snap[d.key()] = d
	}
	return snap
}

// notActive describes the snapshot's deployments that would not pass the
// gate, sorted by workload: "workload support is failed (…)".
func (snap deploymentsSnapshot) notActive() []string {
	var out []string
	for _, d := range snap {
		if deploymentOK(d.Status) {
			continue
		}
		line := "workload " + d.Workload + " is " + d.Status
		if e := strings.TrimSpace(d.Error); e != "" {
			line += " (" + e + ")"
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// gateDeployments is why the deployments after the restart don't pass, or
// "". A deployment that wasn't passing in before is not held against the
// new release; with no snapshot, every deployment counts.
func gateDeployments(before deploymentsSnapshot, after []upgradeDeployment) string {
	for _, d := range after {
		if deploymentOK(d.Status) {
			continue
		}
		if before != nil {
			if b, ok := before[d.key()]; !ok || !deploymentOK(b.Status) {
				continue
			}
		}
		return "workload " + d.Workload + " is " + d.Status + " " + d.Error
	}
	return ""
}

// deploymentsActive is the gate against the live deployments.
func deploymentsActive(s *state, before deploymentsSnapshot) string {
	ds, err := listDeployments(s)
	if err != nil {
		return "deployments: " + err.Error()
	}
	return gateDeployments(before, ds)
}

// notActiveWarnings are the plan warnings for deployments that aren't
// active before the upgrade.
func notActiveWarnings(snap deploymentsSnapshot) []string {
	var out []string
	for _, l := range snap.notActive() {
		out = append(out, l+" before the upgrade: the health check ignores it (`tw deploy` it first to have it checked)")
	}
	return out
}

// wasFailedBefore lists them for the result ("was failed before the upgrade").
func wasFailedBefore(snap deploymentsSnapshot) []string {
	var out []string
	for _, d := range snap {
		if !deploymentOK(d.Status) {
			out = append(out, "workload "+d.Workload+" was "+d.Status+" before the upgrade")
		}
	}
	sort.Strings(out)
	return out
}
