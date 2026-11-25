package mgmt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Smart placement (ADR-033) × containers (ADR-032): scoreHosts keeps
// ADR-032's eligibility filters (the "containers" capability for the images'
// arch, the pinned arches) and the capacity vector's "vms" unit, and its
// path ranking only chooses among the hosts that pass them.
func TestScoreHostsContainers(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(10_000_000)
	ctr := func(id, arch string, nodes, vms int) Candidate {
		return Candidate{ID: id, Tiers: []string{"process", "microvm", CapContainers}, Live: true, Arch: arch,
			Free: map[string]int{"nodes": nodes, "vms": vms}}
	}
	proc := Candidate{ID: "hp", Tiers: []string{"process"}, Live: true, Arch: "amd64", Free: map[string]int{"nodes": 1000, "vms": 0}}
	vmOnly := Candidate{ID: "hv", Tiers: []string{"process", "microvm"}, Live: true, Arch: "amd64", Free: map[string]int{"nodes": 500, "vms": 50}}
	db := []string{"tag:db"}
	meas := func(h, path string, rttMs float64) PathMeasurement {
		return PathMeasurement{HostID: h, Target: "tag:db", Path: path, RTTMicros: int64(rttMs * 1000), MeasuredAt: now.UnixMilli()}
	}
	need := placementCapacity(nil, true)
	for _, tc := range []struct {
		name    string
		cands   []Candidate
		ms      []PathMeasurement
		req     Requirements
		want    string
		why     map[string]string // host → substring of its ineligibility
		summary string
	}{
		{name: "containers needed: only container hosts are eligible",
			cands: []Candidate{proc, vmOnly, ctr("hx", "amd64", 10, 4)},
			req:   Requirements{Containers: true, Capacity: need}, want: "hx",
			why: map[string]string{"hp": "no containers", "hv": "no containers"}},
		{name: "the images' arch",
			cands: []Candidate{ctr("ha", "arm64", 100, 4), ctr("hx", "amd64", 10, 4)},
			req:   Requirements{Containers: true, Arch: "amd64", Capacity: need}, want: "hx",
			why: map[string]string{"ha": "the images need amd64"}},
		{name: "the arches the images were pinned for",
			cands: []Candidate{ctr("ha", "arm64", 100, 4), ctr("hx", "amd64", 10, 4)},
			req:   Requirements{Containers: true, Arches: []string{"amd64"}, Capacity: need}, want: "hx",
			why: map[string]string{"ha": "pinned for amd64"}},
		{name: "a better path never beats eligibility",
			cands: []Candidate{ctr("ha", "arm64", 100, 4), ctr("hx", "amd64", 10, 4)},
			ms:    []PathMeasurement{meas("ha", PathDirect, 1), meas("hx", PathDERP, 40)},
			req:   Requirements{Containers: true, Arch: "amd64", Capacity: need, Targets: db, Now: now}, want: "hx"},
		{name: "path ranking among eligible container hosts",
			cands: []Candidate{ctr("hx", "amd64", 1000, 4), ctr("hy", "amd64", 10, 4)},
			ms:    []PathMeasurement{meas("hx", PathDERP, 40), meas("hy", PathDirect, 2)},
			req:   Requirements{Containers: true, Arch: "amd64", Capacity: need, Targets: db, Now: now}, want: "hy"},
		{name: "no free VM in the capacity vector",
			cands: []Candidate{ctr("hx", "amd64", 1000, 0), ctr("hy", "amd64", 10, 1)},
			req:   Requirements{Containers: true, Capacity: need}, want: "hy",
			why: map[string]string{"hx": "vms: 0 free"}},
		{name: "sticky holder is exempt from capacity, not from containers",
			cands: []Candidate{ctr("hx", "amd64", 0, 0), ctr("hy", "amd64", 100, 4)},
			req:   Requirements{Containers: true, Capacity: need, Sticky: "hx"}, want: "hx"},
		{name: "sticky holder without containers is not kept",
			cands: []Candidate{proc, ctr("hy", "amd64", 100, 4)},
			req:   Requirements{Containers: true, Capacity: need, Sticky: "hp"}, want: "hy"},
		{name: "none eligible: the summary names containers and the arch",
			cands:   []Candidate{proc, ctr("ha", "arm64", 100, 4)},
			req:     Requirements{Containers: true, Arch: "amd64", Capacity: need},
			want:    "",
			summary: "no live host with containers (amd64 images)"},
		{name: "microvm tier + containers",
			cands:   []Candidate{vmOnly},
			req:     Requirements{Tiers: []string{"microvm"}, Containers: true, Capacity: placementCapacity([]string{"microvm"}, true)},
			want:    "",
			summary: "no live host with tier microvm and containers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := scoreHosts(tc.cands, tc.ms, tc.req)
			if d.Host != tc.want {
				t.Fatalf("host %q, want %q (%s)", d.Host, tc.want, d.Summary)
			}
			for h, w := range tc.why {
				found := false
				for _, s := range d.Hosts {
					if s.Host == h {
						found = true
						if s.Eligible || !strings.Contains(s.Why, w) {
							t.Fatalf("%s: eligible %v, why %q, want %q", h, s.Eligible, s.Why, w)
						}
					}
				}
				if !found {
					t.Fatalf("%s not in the decision", h)
				}
			}
			if tc.summary != "" && !strings.Contains(d.Summary, tc.summary) {
				t.Fatalf("summary %q, want %q", d.Summary, tc.summary)
			}
		})
	}
}

// freeUnits maps the capacity vector to placement units; a host that
// predates the vector is not bounded by it.
func TestFreeUnits(t *testing.T) {
	t.Parallel()
	f := freeUnits(Host{CapacityNodes: 10, LiveNodes: 3})
	if f["nodes"] != 7 || f["vms"] != unboundedUnit {
		t.Fatalf("no vector: %v", f)
	}
	f = freeUnits(Host{CapacityNodes: 10, LiveNodes: 3,
		Capacity: &HostCapacity{VMs: 8, VMMemMiB: 8192, VCPUs: 4, VCPUOvercommit: 2, ScratchMiB: 1000},
		Usage:    &HostUsage{VMs: 3, VMMemMiB: 1536, VCPUs: 2.5, ScratchMiB: 100}})
	want := map[string]int{"nodes": 7, "vms": 5, "vmMemMiB": 6656, "vcpus": 5, "scratchMiB": 900, "hugeMemMiB": 0}
	for k, v := range want {
		if f[k] != v {
			t.Fatalf("%s = %d, want %d (%v)", k, f[k], v, f)
		}
	}
	if n := placementCapacity(nil, false); len(n) != 1 || n["nodes"] != 1 {
		t.Fatalf("process need %v", n)
	}
	if n := placementCapacity([]string{"microvm"}, false); n["vms"] != 1 {
		t.Fatalf("microvm need %v", n)
	}
}

// Deploy end to end: the capacity vector a host registered and reported
// gates new placements (not the sticky holder), and `--replace` moves a
// workload that now declares containers off a host without them, where a
// plain redeploy is refused with tier_unavailable.
func TestDeployContainersCapacityAndReplace(t *testing.T) {
	t.Parallel()
	m, tn := tierTestMgmt(t)
	ctx := context.Background()
	full := &HostCapacity{VMs: 2, VMMemMiB: 2048, VCPUs: 4, VCPUOvercommit: 1, ScratchMiB: 1000}
	reg := func(id string, nodes int, tiers []string, c *HostCapacity) {
		t.Helper()
		if _, _, err := m.RegisterHost(ctx, RegisterRequest{HostID: id, CapacityNodes: nodes, Tiers: tiers, Arch: "amd64", Capacity: c}, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	hb := func(id string, u *HostUsage) {
		t.Helper()
		if _, err := m.ReportHostStatus(ctx, id, HostReport{Usage: u}); err != nil {
			t.Fatal(err)
		}
	}
	ctrTiers := []string{"process", "microvm", CapContainers}
	reg("hp", 1000, []string{"process"}, &HostCapacity{})
	reg("hx", 100, ctrTiers, full)
	reg("hy", 10, ctrTiers, full)
	hb("hx", &HostUsage{VMs: 2, VMMemMiB: 2048}) // the roomiest container host has no free VM
	hb("hy", &HostUsage{})

	tw := `{ "tailnet": { "tags": ["tag:tw-web"] } }`
	withCtr := map[string]string{"tailworks.jsonc": tw, "index.js": "export default {}",
		"wrangler.jsonc": `{ "name": "w", "main": "index.js", "containers": [{ "class_name": "S", "image": "i" }],
			"durable_objects": { "bindings": [{ "name": "S", "class_name": "S" }] } }`}
	dep, err := m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "c", Bundle: makeBundle(t, withCtr)})
	if err != nil || dep.Host != "hy" {
		t.Fatalf("containers on the host with a free VM: %+v %v", dep, err)
	}
	// Now hy is full too: the sticky holder keeps the workload, a new one
	// finds no host.
	hb("hy", &HostUsage{VMs: 2, VMMemMiB: 2048})
	if dep, err = m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "c", Bundle: makeBundle(t, withCtr)}); err != nil || dep.Host != "hy" || dep.Placement.Reason != "sticky" {
		t.Fatalf("redeploy on the full sticky holder: %+v %v", dep, err)
	}
	_, err = m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "c2", Bundle: makeBundle(t, withCtr)})
	var e *Error
	if !errors.As(err, &e) || e.Code != "no_host" || !strings.Contains(e.Message, "vms: 0 free") {
		t.Fatalf("new container workload with every VM taken: %v", err)
	}

	// A workload without containers on the process host...
	hb("hy", &HostUsage{})
	plain := map[string]string{"tailworks.jsonc": tw, "index.js": "export default {}"}
	if dep, err = m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "w", Bundle: makeBundle(t, plain)}); err != nil || dep.Host != "hp" {
		t.Fatalf("first deploy: %+v %v", dep, err)
	}
	// ...that now declares containers: refused without --replace...
	_, err = m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "w", Bundle: makeBundle(t, withCtr)})
	if !errors.As(err, &e) || e.Code != "tier_unavailable" || !strings.Contains(e.Message, "containers") || !strings.Contains(e.Message, "--replace") {
		t.Fatalf("redeploy with containers on a host without them: %v", err)
	}
	// ...and moved by --replace (stateless: no DO storage left behind) to
	// the container host with a free VM.
	dep, err = m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "w", Bundle: makeBundle(t, statelessCtr(withCtr)), Replace: true})
	if err != nil || dep.Host != "hy" {
		t.Fatalf("--replace with containers: %+v %v", dep, err)
	}
}

// statelessCtr marks the bundle's classes "fence": "never" so a --replace
// move needs no --force-takeover.
func statelessCtr(files map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	out["tailworks.jsonc"] = `{ "tailnet": { "tags": ["tag:tw-web"], "objects": { "S": { "tags": ["tag:tw-web"], "fence": "never" } } } }`
	return out
}
