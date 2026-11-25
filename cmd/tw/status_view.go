package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// The human `tw status` (#455): the verdict first, then one line per
// component, and the internals (mint queue, pending device deletes, warm
// keys, a non-ready object breakdown, the Worker internet path) only when
// they say something. Everything is still in --json and `tw doctor`.

func statusMark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func statusVerdict(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}

// objectsLabel is "3 objects", with the breakdown when any isn't ready:
// "3 objects (2 ready, 1 pending)".
func objectsLabel(by map[string]int) string {
	n, notReady := 0, false
	var keys []string
	for k, v := range by {
		n += v
		if v > 0 {
			keys = append(keys, k)
			if k != mgmt.StatusReady {
				notReady = true
			}
		}
	}
	label := fmt.Sprintf("%d objects", n)
	if n == 1 {
		label = "1 object"
	}
	if !notReady {
		return label
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == mgmt.StatusReady) != (keys[j] == mgmt.StatusReady) {
			return keys[i] == mgmt.StatusReady
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", by[k], k))
	}
	return label + " (" + strings.Join(parts, ", ") + ")"
}

// mgmtExtras is the mgmt/tenant line's tail: each count only when nonzero
// (warm keys stay visible whenever any are in custody, H25).
func mgmtExtras(mintQueue, pending, warm int) string {
	var b strings.Builder
	if mintQueue > 0 {
		fmt.Fprintf(&b, " · mint queue %d", mintQueue)
	}
	if pending == 1 {
		b.WriteString(" · 1 pending device delete")
	} else if pending > 0 {
		fmt.Fprintf(&b, " · %d pending device deletes", pending)
	}
	if warm > 0 {
		b.WriteString(" · " + warmKeysLabel(warm))
	}
	return b.String()
}

// printWorkerInternetNotable prints tw status's internet line only when it
// is worth reading: a warning, or a path other than direct.
func printWorkerInternetNotable(saved, running, problem string) {
	kind, _, _ := hostdrun.ParseInternetMode(cmpOr(running, saved))
	if kind == workerInternetDirect && problem == "" && (running == "" || running == saved) {
		return
	}
	printWorkerInternet(saved, running, problem) // up_workerinternet.go
}

// printHostStatus is `tw status` on the host.
func printHostStatus(s *state, r statusResult, hv hostd.StatusView, hostOK bool) {
	fmt.Printf("tw status · %s · %s · tailnet %s\n", statusVerdict(r.Healthy), s.Dir, s.Cfg.Tailnet)
	fmt.Printf("%s supervisor  pid %d\n", statusMark(r.Supervisor), s.supervisorPID())
	if r.MgmtNode != "" {
		sch := "http"
		if s.Cfg.HTTPS {
			sch = "https"
		}
		fmt.Printf("%s node        %s (%s; API over the tailnet, WhoIs auth) · console %s://%s/ui/\n", statusMark(r.Mgmt), r.MgmtNode, mgmt.MgmtTag, sch, r.MgmtNode)
	}
	fmt.Printf("%s mgmt        tenant %s%s\n", statusMark(r.Mgmt), r.TenantID, mgmtExtras(r.MintQueue, r.Pending, r.WarmKeys))
	where := "own node " + mgmt.HostNodeName(s.Cfg.HostID)
	switch {
	case s.Cfg.single():
		where = "embedded in the mgmt process"
	case s.Cfg.LocalDev:
		where = "local-dev"
	}
	fmt.Printf("%s host        %s (%s) · heartbeat %.1fs ago · %s\n", statusMark(r.Host), s.Cfg.HostID, where, float64(r.HostAgeMS)/1000, objectsLabel(r.Objects))
	printWorkerInternetNotable(s.Cfg.workerInternet(), hv.WorkerInternet, hv.WorkerInternetProblem)
	var running *bool
	if hostOK {
		running = &hv.InsecureSharedHost
	}
	printSavedFlags(s.Cfg, s.Dir, 12, running) // up_unsafe.go
	if len(r.Tenants) > 1 {
		for _, t := range r.Tenants {
			fmt.Printf("  tenant      %s (%s) · %s%s\n", t.Tailnet, t.TenantID, objectsLabel(t.Objects), mgmtExtras(t.MintQueue, 0, t.WarmKeys))
		}
	}
	for _, w := range r.Workloads {
		e := ""
		if w.Error != "" {
			e = " · " + tsafe(w.Error)
		}
		sb := ""
		if w.Sandbox != "" {
			sb = ", sandbox " + w.Sandbox
		}
		if w.Egress != "" {
			sb += ", egress " + w.Egress
			if w.Egress == hostd.EgressUnenforced {
				sb += " (Node runtime: the egress proxy is cooperative here, not a boundary)"
			}
		}
		fmt.Printf("%s workload    %s %s (tenant %s, %s, epoch %d, %d requests%s)%s\n", statusMark(w.Status == "active"), w.Workload, w.Status, w.TenantID, w.DeploymentID, w.Epoch, w.Requests, sb, e)
	}
	printDegradedStores(r.DegradedStores)
	printContainers(r.Capacity, r.Reservations, r.Containers, hv.ImageCacheBytes)
}

// remoteTenant is one tenant of `tw status` on a linked machine.
type remoteTenant struct {
	TenantRef
	Deployments []mgmt.DeploymentView `json:"deployments"`
}

// printRemoteStatus is `tw status` on a linked machine: the hosts, and per
// tenant how many deployments (tw ls lists them) plus any that needs a
// look (an error, or not active).
func printRemoteStatus(s *state, w *mgmt.WhoAmI, hosts []mgmt.Host, tenants []remoteTenant, live int,
	degraded []mgmt.StoreInfo, capv []hostCapacity, resv []mgmt.ContainerReservation) {
	fmt.Printf("tw status · %s · linked to %s · tailnet %s · you are %s (admin by WhoIs)\n", statusVerdict(live > 0), s.Link.Endpoint, w.Mgmt.Tailnet, w.Actor)
	for _, h := range hosts {
		fmt.Printf("%s host        %s · heartbeat %.1fs ago · %d live nodes · offers %s\n", statusMark(h.Live(time.Now())), h.ID, time.Since(time.UnixMilli(h.LastSeen)).Seconds(), h.LiveNodes, strings.Join(h.Tiers, ","))
	}
	for _, t := range tenants {
		n := fmt.Sprintf("%d deployments", len(t.Deployments))
		if len(t.Deployments) == 1 {
			n = "1 deployment"
		}
		fmt.Printf("✓ tenant      %s (%s, mint mode %s) · %s (tw ls)\n", t.Tailnet, t.TenantID, mintModeName(t.MintMode), n)
		for _, d := range t.Deployments {
			if d.Error == "" && d.Status == "active" {
				continue
			}
			e := ""
			if d.Error != "" {
				e = " · " + tsafe(d.Error)
			}
			fmt.Printf("! workload    %s %s · http://%s/%s\n", tsafe(d.Workload), tsafe(d.Status), tsafe(d.Endpoint.MagicDNS), e)
		}
	}
	printDegradedStores(degraded)
	printContainers(capv, resv, nil, 0)
}

// printImagePins is `tw ls`'s line per image a deployment's container
// classes pinned at deploy (ADR-032 D3).
func printImagePins(d mgmt.DeploymentView) {
	for _, c := range d.Containers {
		for _, p := range c.Pins {
			fmt.Printf("  %-12s image: %s %s linux/%s → %s@%s\n", "", tsafe(c.Class), tsafe(p.Ref), p.Arch, tsafe(p.Repository), p.Digest)
		}
	}
}
