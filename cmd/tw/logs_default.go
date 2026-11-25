package main

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/replicaname"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// componentsFix is the fix line naming the daemons' logs, which a bare
// `tw logs` never shows.
const componentsFix = "tw logs --component mgmt|hostd|supervisor   (the daemons' logs)"

// defaultLogsWorkload is the workload a bare `tw logs` shows: the app in
// the current directory, else the tenant's only deployed workload
// (previews and replicas count as their base workload). Otherwise it fails
// bad_request with one `tw logs <workload>` per workload. It never falls
// back to a daemon's log: that takes --component.
func defaultLogsWorkload(s *state, tenantRef string) (string, error) {
	if a, err := loadApp(".", ""); err == nil {
		return a.Name, nil
	}
	tn, err := s.tenant(tenantRef)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var names []string
	if s.remote() {
		deps, err := listAll[mgmt.DeploymentView](ctx, s.mgmt(), "/v1/deployments?latest=true&tenant="+url.QueryEscape(tn.TenantID), 0)
		if err != nil {
			return "", asCLIErr(err).withFix("tw logs <workload>", "on the host: "+componentsFix)
		}
		for _, d := range deps {
			if d.Status != "stopped" {
				names = append(names, d.Workload)
			}
		}
	} else {
		var hv hostd.StatusView
		if err := s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv); err != nil {
			return "", fail("host_unavailable", "tw-hostd is not answering", err.Error()).withFix("tw status", componentsFix)
		}
		for _, w := range hv.Workloads {
			if w.TenantID == tn.TenantID {
				names = append(names, w.Workload)
			}
		}
	}
	bases := baseWorkloads(names)
	if len(bases) == 1 {
		return bases[0], nil
	}
	msg := "no app in the current directory and no deployed workload: name a workload or a component"
	if len(bases) > 1 {
		msg = "no app in the current directory and more than one deployed workload: name one"
	}
	var fix []string
	for _, b := range bases {
		fix = append(fix, "tw logs "+b)
	}
	if s.remote() {
		fix = append(fix, "on the host: "+componentsFix)
	} else {
		fix = append(fix, componentsFix)
	}
	return "", fail("bad_request", msg).withFix(fix...)
}

// baseWorkloads maps preview (<w>--preview-<alias>) and replica (<w>--rN)
// workload names to their base workload, sorted and deduplicated.
func baseWorkloads(names []string) []string {
	var out []string
	for _, n := range names {
		if g, ok := replicaname.Group(n); ok {
			n = g
		}
		if b, _, ok := strings.Cut(n, "--preview-"); ok && b != "" {
			n = b
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
