package main

// `tw deployments <workload>` (ADR-046): a workload's
// deployment history, newest first (GET
// /v1/tenants/{id}/workloads/{workload}/deployments).

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

type deploymentsResult struct {
	Workload        string                  `json:"workload"`
	Tenant          string                  `json:"tenant"`
	TenantID        string                  `json:"tenantID"`
	BundleRetention int                     `json:"bundleRetention"`
	Deployments     []mgmt.DeploymentRecord `json:"deployments"`
}

type historyPage struct {
	Items           []mgmt.DeploymentRecord `json:"items"`
	NextCursor      *string                 `json:"nextCursor"`
	BundleRetention int                     `json:"bundleRetention"`
}

func cmdDeployments(args []string) error {
	f := newFlags("deployments")
	tenantF := tenantFlag(f)
	limit := f.Int("limit", 20, "show the newest N deployments (0 = all)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw deployments needs one workload", "usage: tw "+usageOf("deployments"))
	}
	if *limit < 0 {
		return fail("bad_request", "--limit must be 0 (all) or more")
	}
	workload := pos[0]
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := deploymentsResult{Workload: workload, Tenant: tn.Tailnet, TenantID: tn.TenantID, Deployments: []mgmt.DeploymentRecord{}}
	base := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/deployments"
	cursor := ""
	for {
		n := 500
		if *limit > 0 {
			n = min(n, *limit-len(res.Deployments))
		}
		u := base + "?limit=" + strconv.Itoa(n)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var pg historyPage
		if err := s.mgmt().Do(ctx, "GET", u, nil, &pg); err != nil {
			return err
		}
		res.BundleRetention = pg.BundleRetention
		res.Deployments = append(res.Deployments, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" || (*limit > 0 && len(res.Deployments) >= *limit) {
			break
		}
		cursor = *pg.NextCursor
	}
	emit(f, res, func() { printDeployments(res) })
	return nil
}

func printDeployments(res deploymentsResult) {
	fmt.Printf("%-22s %-19s %-12s %-12s %-22s %s\n", "DEPLOYMENT", "CREATED", "BUNDLE", "STATUS", "HOST", "BY")
	for i, d := range res.Deployments {
		bundle := "-"
		if len(d.BundleSHA256) >= 12 {
			bundle = d.BundleSHA256[:12]
		}
		if !d.BundleRetained && d.BundleSHA256 != "" {
			bundle += "*"
		}
		st := d.Status
		if (st == "superseded" || st == "stopped") && d.HostStatus == "failed" {
			st += " (failed)" // it never served: rollback skips it
		}
		mark := d.ID
		if i == 0 && d.Status != "superseded" && d.Status != "stopped" {
			mark += " ←"
		}
		fmt.Printf("%-22s %-19s %-12s %-12s %-22s %s\n", tsafe(mark), time.UnixMilli(d.CreatedAt).Local().Format("2006-01-02 15:04:05"),
			tsafe(bundle), tsafe(st), tsafe(d.HostID), tsafe(d.DeployedBy))
		if d.RollbackOf != "" {
			fmt.Printf("  rollback of %s\n", tsafe(d.RollbackOf))
		}
		if d.Error != "" && (d.Status == "failed" || d.HostStatus == "failed") {
			fmt.Printf("  error: %s\n", tsafe(firstLine(d.Error)))
		}
	}
	keep := "every bundle"
	if res.BundleRetention > 0 {
		keep = fmt.Sprintf("the newest %d bundles", res.BundleRetention)
	}
	fmt.Printf("\n← current · * bundle pruned (mgmt keeps %s per workload) · tw rollback %s [<deployment>]\n", keep, res.Workload)
}
