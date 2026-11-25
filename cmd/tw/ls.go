package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw ls [workload] (#452, #459): a tenant's deployments and objects.
//
// By default it lists every live object (pending, minted, assigned, ready:
// mgmt.LiveStatuses, filtered by mgmt) and every deployment that isn't
// stopped. --all adds killed objects and stopped deployments, and nothing
// else. Both follow pages up to lsCeiling objects (--limit; 0 = none) and
// say when they stopped there: "truncated" in --json, a "… and N more" line
// in the human output.
//
// A workload argument keeps that workload's deployment and the deployments
// whose _workload namespace says they are its previews or replicas
// (ClassConfig.PreviewOf / ReplicaOf, never the "--preview-" name), and
// fetches only those workloads' objects from mgmt, so the ceiling applies to
// the selection.

// lsCeiling is how many objects tw ls lists at most by default.
const lsCeiling = 5000

// lsResult is tw ls --json (schema/cli.schema.json $defs/ls).
type lsResult struct {
	Tenant      string                `json:"tenant"`
	TenantID    string                `json:"tenantID"`
	Deployments []mgmt.DeploymentView `json:"deployments"`
	Objects     []mgmt.Object         `json:"objects"`
	Nodes       *nodeBudget           `json:"nodes"`
	// Truncated: more objects matched than the ceiling (--limit) let through.
	Truncated bool `json:"truncated"`
}

func cmdLs(args []string) error {
	f := newFlags("ls")
	all := f.Bool("all", false, "also list killed objects and stopped deployments")
	limit := f.Int("limit", lsCeiling, "list at most this many objects (0: no ceiling)")
	tenantF := tenantFlag(f)
	f.group(advancedFlags, "limit") // helpflags.go
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw ls takes at most one workload", "usage: tw "+usageOf("ls"))
	}
	workload := ""
	if len(pos) == 1 {
		workload = pos[0]
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deps, err := listAll[mgmt.DeploymentView](ctx, s.mgmt(), "/v1/deployments?latest=true&tenant="+url.QueryEscape(tn.TenantID), 0)
	if err != nil {
		return err
	}
	// The workloads whose objects are listed ("" = the whole tenant).
	names := []string{""}
	hasDeployment := false
	if workload != "" {
		deps = slices.DeleteFunc(deps, func(d mgmt.DeploymentView) bool { return !lsSelects(d, workload) })
		hasDeployment = len(deps) > 0
		names = []string{workload}
		for _, d := range deps {
			if !slices.Contains(names, d.Workload) {
				names = append(names, d.Workload)
			}
		}
	}
	if !*all {
		deps = slices.DeleteFunc(deps, func(d mgmt.DeploymentView) bool { return d.Status == "stopped" })
	}
	objs, truncated, err := s.lsObjects(ctx, tn.TenantID, names, *all, *limit)
	if err != nil {
		return err
	}
	// A stopped deployment still names the workload (tw ls <w> --all shows it).
	if workload != "" && !hasDeployment && len(objs) == 0 {
		return fail("unknown_workload", fmt.Sprintf("tenant %s has no workload %s", tn.Tailnet, workload), "tw ls lists the tenant's workloads")
	}
	nodes := s.nodeBudgetOf(ctx, tn, nil, false) // nodebudget.go: every live node, whatever --limit
	res := lsResult{Tenant: tn.Tailnet, TenantID: tn.TenantID, Deployments: deps, Objects: objs, Nodes: nodes, Truncated: truncated}
	emit(f, res, func() {
		fmt.Printf("TENANT %s (%s)\n", tn.Tailnet, tn.TenantID)
		printLsNodes(nodes) // nodebudget.go
		fmt.Println("DEPLOYMENTS")
		for _, d := range deps {
			printLsDeployment(d)
		}
		fmt.Println("OBJECTS")
		fmt.Printf("  %-36s %-34s %-8s %-30s %s\n", "NODE", "CLASS/NAME", "STATUS", "TAGS", "PARENT")
		by := map[string]mgmt.Object{}
		for _, o := range objs {
			by[o.ID] = o
		}
		for _, o := range objs {
			parent := o.ParentID // not listed (killed, another workload, past the ceiling): its ID
			if p, ok := by[o.ParentID]; ok {
				parent = nodeName(p)
			}
			fmt.Printf("  %-36s %-34s %-8s %-30s %s\n", tsafe(nodeName(o)), tsafe(displayNS(o)), tsafe(o.Status), tsafe(strings.Join(o.Tags, ",")), tsafe(parent))
		}
		if truncated {
			fmt.Printf("  %s\n", lsMoreLine(len(objs), workload == "" && !*all, nodes))
		}
	})
	return nil
}

// lsSelects: d is workload w's deployment, or one of its previews or
// replicas (as their _workload namespace records it).
func lsSelects(d mgmt.DeploymentView, w string) bool {
	if d.Workload == w {
		return true
	}
	for _, c := range d.Namespaces {
		if c.Class == mgmt.WorkloadNamespace && (c.PreviewOf == w || c.ReplicaOf == w) {
			return true
		}
	}
	return false
}

// lsObjects fetches the objects of the named workloads ("" = every
// workload) in creation order: live ones only unless all, at most max
// (0 = no ceiling). truncated says more matched.
func (s *state) lsObjects(ctx context.Context, tenantID string, names []string, all bool, max int) ([]mgmt.Object, bool, error) {
	q := url.Values{"tenant": {tenantID}}
	if !all {
		q["state"] = mgmt.LiveStatuses
	}
	out := []mgmt.Object{}
	truncated := false
	for _, w := range names {
		if w != "" {
			q.Set("workload", w)
		}
		left := 0
		if max > 0 {
			if left = max - len(out); left <= 0 {
				// The ceiling is reached: only ask whether this workload has more.
				if more, _, err := listUpTo[mgmt.Object](ctx, s.mgmt(), "/v1/objects?"+q.Encode(), 1); err != nil {
					return out, false, err
				} else if len(more) > 0 {
					truncated = true
					break
				}
				continue
			}
		}
		got, more, err := listUpTo[mgmt.Object](ctx, s.mgmt(), "/v1/objects?"+q.Encode(), left)
		if err != nil {
			return out, false, err
		}
		out = append(out, got...)
		truncated = truncated || more
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, truncated, nil
}

// printLsDeployment is a deployment's DEPLOYMENTS line and its sub-lines.
func printLsDeployment(d mgmt.DeploymentView) {
	by := ""
	if d.DeployedBy != "" {
		by = "  by " + tsafe(d.DeployedBy)
	}
	fmt.Printf("  %-12s %-22s %-8s %-10s http://%s/%s\n", tsafe(d.Workload), tsafe(d.DeploymentID), tsafe(d.Host), tsafe(d.Status), tsafe(d.Endpoint.MagicDNS), by)
	if d.Placement != nil && d.Placement.Summary != "" {
		fmt.Printf("  %-12s placement: %s\n", "", tsafe(d.Placement.Summary))
	}
	for _, c := range d.Namespaces {
		if c.Class == mgmt.WorkloadNamespace && c.PreviewOf != "" {
			alias := strings.TrimPrefix(d.Workload, mgmt.PreviewName(c.PreviewOf, ""))
			fmt.Printf("  %-12s preview of %s (alias %s) · tw promote %s %s\n", "", tsafe(c.PreviewOf), tsafe(alias), tsafe(c.PreviewOf), tsafe(alias))
		}
	}
	if d.Error != "" {
		fmt.Printf("  %-12s error: %s\n", "", tsafe(deployErrorLine(d.Workload, d.Error))) // lserror.go
	}
	printImagePins(d) // status_view.go
}

// lsMoreLine is the line under a truncated OBJECTS list. When the list is
// the tenant's live objects, the node count says how many were left out.
func lsMoreLine(shown int, liveTenant bool, nodes *nodeBudget) string {
	const hint = "(tw ls --limit 0 lists them all)"
	if liveTenant && nodes != nil && nodes.Tenant != nil && nodes.Tenant.Total > shown {
		return fmt.Sprintf("… and %d more live objects %s", nodes.Tenant.Total-shown, hint)
	}
	return "… and more objects " + hint
}
