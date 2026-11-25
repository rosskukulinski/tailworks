package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Workflow approvals (ADR-040, docs/approvals.md):
//
//	tw approvals [workload] [--all]                        what waits for a decision
//	tw approve <workload> <instance> [--reject] [--comment text] [--workflow name] [--step name]
//
// Both go through mgmt (GET /v1/tenants/{id}/approvals, POST …/approvals/
// {workflow}/{instance}). The decision is made by this machine's own
// tailnet identity (`tw link`: mgmt's WhoIs), checked against its
// github.com/rosskukulinski/tailworks/cap/approve grants by the host running the Workflow. On the tailworks
// host itself (the operator console) there is no personal identity, so
// approve is refused there; listing works.

// approvalTenant resolves --tenant. An approver who is not a tailworks admin
// cannot list tenants, so a tenant ID ("tn_…") is taken as is, and from a
// linked machine the default (and any tailnet name) comes from GET
// /v1/whoami: the tenants on this machine's own tailnet (issue #50).
func approvalTenant(s *state, ref string) (string, error) {
	if strings.HasPrefix(ref, "tn_") {
		return ref, nil
	}
	if s.remote() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		w, err := s.whoami(ctx)
		cancel()
		if err != nil {
			return "", err
		}
		if id, ok, err := pickWhoTenant(w.Tenants, ref); ok || err != nil {
			return id, err
		}
	}
	tn, err := s.tenant(ref)
	if err != nil {
		if e, ok := err.(*cliErr); ok {
			return "", e.withFix("tw approvals --tenant <tenant ID>   (approvers who are not tw admins: ask an admin for the tenant ID)")
		}
		return "", err
	}
	return tn.TenantID, nil
}

// pickWhoTenant chooses among whoami's tenants: the only one when ref is
// "", else the one ref names (tailnet, its first label, or ID). ok is false
// when whoami's list doesn't decide it (then the caller falls back).
func pickWhoTenant(ts []mgmt.WhoAmITenant, ref string) (id string, ok bool, err error) {
	ref = strings.TrimSuffix(ref, ".")
	if ref == "" {
		switch len(ts) {
		case 0:
			return "", false, nil
		case 1:
			return ts[0].TenantID, true, nil
		}
		var names []string
		for _, t := range ts {
			names = append(names, t.Tailnet+" ("+t.TenantID+")")
		}
		return "", true, fail("bad_request", "your tailnet has several tenants: "+strings.Join(names, ", ")).withFix("tw approvals --tenant <tailnet or tenant ID>")
	}
	for _, t := range ts {
		label, _, _ := strings.Cut(t.Tailnet, ".")
		if ref == t.Tailnet || ref == t.TenantID || ref == label {
			return t.TenantID, true, nil
		}
	}
	return "", false, nil
}

func listApprovals(ctx context.Context, s *state, tenantID, workload string, all bool) (v1.ApprovalList, error) {
	q := url.Values{}
	if workload != "" {
		q.Set("workload", workload)
	}
	if all {
		q.Set("all", "true")
	}
	var out v1.ApprovalList
	err := s.mgmt().Do(ctx, "GET", "/v1/tenants/"+url.PathEscape(tenantID)+"/approvals?"+q.Encode(), nil, &out)
	return out, err
}

func cmdApprovals(args []string) error {
	f := newFlags("approvals")
	all := f.Bool("all", false, "also show decided and timed-out approvals the hosts still keep")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw approvals takes at most one workload", "usage: tw "+usageOf("approvals"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tenantID, err := approvalTenant(s, *tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wl := ""
	if len(pos) == 1 {
		wl = pos[0]
	}
	out, err := listApprovals(ctx, s, tenantID, wl, *all)
	if err != nil {
		return err
	}
	emit(f, out, func() {
		if len(out.Approvals) == 0 {
			fmt.Println("no approvals waiting")
		} else {
			fmt.Printf("%-16s %-16s %-28s %-20s %-8s %-9s %s\n", "WORKLOAD", "WORKFLOW", "INSTANCE", "STEP", "AGE", "STATUS", "PARAMS / DECIDED BY")
			for _, a := range out.Approvals {
				extra := factsString(a.Facts)
				if a.Approver != nil {
					extra = approverString(*a.Approver)
					if a.Comment != nil && *a.Comment != "" {
						extra += fmt.Sprintf(" %q", *a.Comment)
					}
				}
				// Instance IDs, step names and params are tenant code's (and
				// may come from an internet request): escaped.
				fmt.Printf("%-16s %-16s %-28s %-20s %-8s %-9s %s\n", tsafe(a.Workload), tsafe(a.Workflow), tsafe(a.Instance), tsafe(a.Step), shortAge(time.Since(a.RequestedAt)), a.Status, tsafe(extra))
			}
		}
		if out.Unreachable != nil && len(*out.Unreachable) > 0 {
			fmt.Printf("warning: host(s) %s did not answer; their approvals are missing\n", tsafe(strings.Join(*out.Unreachable, ", ")))
		}
	})
	return nil
}

func cmdApprove(args []string) error {
	f := newFlags("approve")
	reject := f.Bool("reject", false, "reject instead of approve")
	comment := f.String("comment", "", "shown to the Workflow and recorded in the audit log")
	workflow := f.String("workflow", "", "the Workflow's name (default: the one whose instance waits)")
	step := f.String("step", "", "which wait, when the instance waits for several")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return fail("bad_request", "tw approve needs a workload and an instance ID", "usage: tw "+usageOf("approve"))
	}
	wl, inst := pos[0], pos[1]
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tenantID, err := approvalTenant(s, *tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wf := *workflow
	if wf == "" {
		list, err := listApprovals(ctx, s, tenantID, wl, false)
		if err != nil {
			return err
		}
		names := map[string]bool{}
		for _, a := range list.Approvals {
			if a.Instance == inst && a.Status == v1.ApprovalStatusPending {
				names[a.Workflow] = true
			}
		}
		switch len(names) {
		case 0:
			return fail("approval_not_pending", fmt.Sprintf("no approval of instance %s of %s is waiting (or your grants do not cover it)", inst, wl)).
				withFix("tw approvals " + wl)
		case 1:
			for n := range names {
				wf = n
			}
		default:
			ns := make([]string, 0, len(names))
			for n := range names {
				ns = append(ns, n)
			}
			sort.Strings(ns)
			return fail("bad_request", "instance "+inst+" waits in several Workflows ("+strings.Join(ns, ", ")+")").withFix("tw approve " + wl + " " + inst + " --workflow <name>")
		}
	}
	decision := "approve"
	if *reject {
		decision = "reject"
	}
	body := map[string]any{"decision": decision}
	if *comment != "" {
		body["comment"] = *comment
	}
	if *step != "" {
		body["step"] = *step
	}
	var out v1.ApprovalDecisionResponse
	path := "/v1/tenants/" + url.PathEscape(tenantID) + "/workloads/" + url.PathEscape(wl) + "/approvals/" + url.PathEscape(wf) + "/" + url.PathEscape(inst)
	if err := s.mgmt().Do(ctx, "POST", path, body, &out); err != nil {
		return err
	}
	emit(f, out, func() {
		a := out.Approval
		by := ""
		if a.Approver != nil {
			by = " as " + approverString(*a.Approver)
		}
		fmt.Printf("✓ %s %s/%s/%s (step %s)%s\n", a.Status, tsafe(a.Workload), tsafe(a.Workflow), tsafe(a.Instance), tsafe(a.Step), tsafe(by))
	})
	return nil
}

func approverString(a v1.Approver) string {
	who := a.User
	if who == nil || *who == "" {
		return strings.TrimSuffix(a.Node, ".") + " " + fmt.Sprint(a.Tags)
	}
	return *who + " (" + strings.TrimSuffix(a.Node, ".") + ")"
}

func factsString(f *map[string]float64) string {
	if f == nil || len(*f) == 0 {
		return ""
	}
	var parts []string
	for _, k := range sortedKeys(*f) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, (*f)[k]))
	}
	return strings.Join(parts, " ")
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
