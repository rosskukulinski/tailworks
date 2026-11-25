package mgmt

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/replicaname"
)

// Replicas (tailworks.jsonc "replicas": N) run one stateless workload on N
// distinct hosts, usually behind one Tailscale Service (tailnet.service).
//
// Each extra replica is deployed as a workload of its own, named
// ReplicaName(workload, i): "<workload>--r2", "<workload>--r3", … The first
// replica is the workload itself, so "replicas": 1 is exactly the old
// behavior. That keeps every per-workload mechanism unchanged: each replica
// has its own namespace lease (and epoch), deployment record, assignment,
// workload node and identity. The _workload namespace of replicas 2..N
// records the group (ClassConfig.ReplicaOf); the workload's own records the
// count (ClassConfig.Replicas).
//
// Only stateless workloads may have replicas: a Durable Object must run in
// exactly one place (ADR-028), which is what the lease and host fencing
// enforce. A bundle with any DO class not marked "fence": "never" is
// refused.

// MaxReplicas bounds tailworks.jsonc "replicas".
const MaxReplicas = 16

// replicaSep and ReplicaName are internal/replicaname's: hostd reads a
// replica's name with the same package (hostd/r2.go r2Group).
const replicaSep = replicaname.Sep

// ReplicaName is the workload name of replica i (0-based) of workload.
func ReplicaName(workload string, i int) string { return replicaname.Name(workload, i) }

// replicaIndex parses a ReplicaName of group (0-based; -1 if it isn't one).
func replicaIndex(group, name string) int {
	if name == group {
		return 0
	}
	rest, ok := strings.CutPrefix(name, group+replicaSep)
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || strconv.Itoa(n) != rest {
		return -1
	}
	return n - 1
}

// replicaGroup returns the workload a deployment is a replica of ("" for a
// workload's own deployment).
func replicaGroup(classes []ClassConfig) string {
	for _, c := range classes {
		if c.Class == WorkloadNamespace {
			return c.ReplicaOf
		}
	}
	return ""
}

// checkReplicas validates tailworks.jsonc "replicas" for a deploy of r.Workload
// and returns the replica count.
func (m *Mgmt) checkReplicas(ctx context.Context, t Tenant, r DeployRequest, cfg *TwConfig, classes []ClassConfig, stateful bool) (int, error) {
	n := 1
	if cfg != nil && cfg.Replicas != 0 {
		n = cfg.Replicas
	}
	if n < 1 || n > MaxReplicas {
		return 0, errf(400, "bad_bundle", "tailworks.jsonc: replicas must be 1..%d (got %d)", MaxReplicas, n)
	}
	latest, err := m.store.LatestDeployments(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	live := map[string]Deployment{}
	for _, d := range latest {
		if d.StoppedAt == 0 {
			live[d.Workload] = d
		}
	}
	// A replica is deployed through its workload, never by its own name.
	if d, ok := live[r.Workload]; ok {
		if g := replicaGroup(d.Namespaces); g != "" {
			return 0, errf(409, "replica_conflict", "%q is replica %d of workload %q; deploy %q instead (tailworks.jsonc \"replicas\")",
				r.Workload, replicaIndex(g, r.Workload)+1, g, g)
		}
	}
	if n == 1 {
		return 1, nil
	}
	if stateful {
		_, why := BundleStateful(r.Bundle)
		return 0, errf(400, "bad_bundle",
			"tailworks.jsonc: replicas: %d needs a stateless workload, but this bundle has Durable Objects (%s) that must run in exactly one place; drop \"replicas\", or mark classes that tolerate several instances \"fence\": \"never\"",
			n, why)
	}
	if r.Host != "" {
		return 0, errf(400, "bad_request", "--host pins one host; a workload with replicas: %d is placed on %d distinct hosts", n, n)
	}
	if !validName(ReplicaName(r.Workload, n-1)) {
		return 0, errf(400, "bad_request", "workload name %q is too long for %d replicas (replica names are <workload>%s<n>, ≤ 63 characters)", r.Workload, n, replicaSep)
	}
	if !hasWorkloadNode(classes) {
		return 0, errf(400, "bad_bundle", "tailworks.jsonc: replicas needs tailnet.tags: each replica is reached through its own workload node")
	}
	for i := 1; i < n; i++ {
		name := ReplicaName(r.Workload, i)
		if d, ok := live[name]; ok && replicaGroup(d.Namespaces) != r.Workload {
			return 0, errf(409, "replica_conflict", "replica %d of %q would be named %q, which is already a separate workload; undeploy it or rename one of them", i+1, r.Workload, name)
		}
	}
	return n, nil
}

// checkReplicaName refuses a deploy of a workload that is named like a
// replica ("<x>--r<n>", replicaname.Group) and is not one.
//
// hostd finds the workload whose R2 blobs a replica shares from the
// replica's name alone (hostd/r2.go r2Group), while a bucket's index, the
// other stores and the secrets follow the deployment record
// (replicaGroupOf). A workload of its own called "web--r2" would keep its
// bucket's index to itself and write its bytes under web's blob prefix
// (#348, ledger H-r2group). So such a name belongs to replicas: both sides
// read it with replicaname.Group, and for every name a deploy accepts they
// agree.
//
// A workload deployed under such a name before this check is not renamed:
// while it is live it keeps deploying.
func (m *Mgmt) checkReplicaName(ctx context.Context, r DeployRequest) error {
	g, ok := replicaname.Group(r.Workload)
	if !ok {
		return nil
	}
	if rc := r.replica; rc != nil && rc.index > 0 && r.Workload == ReplicaName(rc.group, rc.index) {
		return nil // a replica, deployed through its workload
	}
	d, err := m.latestDeployment(ctx, r.TenantID, r.Workload)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && d.StoppedAt == 0 { // live under this name
		of := replicaGroup(d.Namespaces)
		if of == "" {
			return nil // a workload from before this check
		}
		return errf(409, "replica_conflict", "%q is replica %d of workload %q; deploy %q instead (tailworks.jsonc \"replicas\")",
			r.Workload, replicaIndex(of, r.Workload)+1, of, of)
	}
	if pc := r.preview; pc != nil && pc.of != "" {
		return errf(409, "replica_conflict", "the preview would be the workload %q, and a name ending in %s<n> belongs to the replicas of a workload (here %q); pick another --preview-alias",
			r.Workload, replicaname.Sep, g)
	}
	return errf(409, "replica_conflict", "%q is named like a replica of %q: a name ending in %s<n> (n ≥ 2) belongs to a workload's replicas (tailworks.jsonc \"replicas\"); rename the workload",
		r.Workload, g, replicaname.Sep)
}

func hasWorkloadNode(classes []ClassConfig) bool {
	for _, c := range classes {
		if c.Class == WorkloadNamespace {
			return true
		}
	}
	return false
}

// replicasOf returns the live latest deployments that are replicas 2.. of
// group, by index.
func (m *Mgmt) replicasOf(ctx context.Context, tenantID, group string) (map[int]Deployment, error) {
	latest, err := m.store.LatestDeployments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := map[int]Deployment{}
	for _, d := range latest {
		if d.StoppedAt != 0 || replicaGroup(d.Namespaces) != group {
			continue
		}
		if i := replicaIndex(group, d.Workload); i > 0 {
			out[i] = d
		}
	}
	return out, nil
}

// stopExtraReplicas undeploys replicas n+1.. of group (a scale-down).
func (m *Mgmt) stopExtraReplicas(ctx context.Context, tenantID, group string, n int, by string) {
	reps, err := m.replicasOf(ctx, tenantID, group)
	if err != nil {
		m.log.Warn("scale-down: can't list replicas", "workload", group, "err", err)
		return
	}
	for i, d := range reps {
		if i < n {
			continue
		}
		if _, err := m.Undeploy(ctx, d.ID, fmt.Sprintf("scaled down to %d replica(s)", n), by); err != nil {
			m.log.Warn("scale-down: undeploy failed", "replica", d.Workload, "err", err)
		}
	}
}

// replicaCtx tells the inner Deploy which replica it is deploying.
type replicaCtx struct {
	group   string // the workload the replicas belong to
	index   int    // 0-based; 0 is the workload itself
	n       int    // replica count
	exclude map[string]bool
}

// mark records the group on the _workload namespace (a nil receiver is a
// no-op).
func (rc *replicaCtx) mark(classes []ClassConfig) {
	if rc == nil {
		return
	}
	for j := range classes {
		if classes[j].Class != WorkloadNamespace {
			continue
		}
		classes[j].ReplicaOf, classes[j].Replicas = "", 0
		if rc.index > 0 {
			classes[j].ReplicaOf = rc.group
		} else if rc.n > 1 {
			classes[j].Replicas = rc.n
		}
	}
}

// owner is the workload a shared Service belongs to: the group.
func (rc *replicaCtx) owner(workload string) string {
	if rc == nil || rc.group == "" {
		return workload
	}
	return rc.group
}

// deployGroup is Deploy's entry point: it reads tailworks.jsonc "replicas",
// places every replica on a distinct host (smart placement, ADR-033, with
// the other replicas' hosts excluded) before writing anything, deploys each
// replica as its own workload, then stops replicas past N.
func (m *Mgmt) deployGroup(ctx context.Context, r DeployRequest) (DeployResponse, error) {
	var resp DeployResponse
	t, err := m.store.GetTenant(ctx, r.TenantID)
	if err != nil {
		return resp, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if !validName(r.Workload) {
		return resp, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	var cfg *TwConfig
	classes := slices.Clone(r.Namespaces)
	stateful := true
	if len(r.Bundle) > 0 {
		if c, err := BundleConfig(r.Bundle); err == nil {
			cfg = c
			if len(classes) == 0 {
				classes = c.Classes()
			}
		}
		stateful, _ = BundleStateful(r.Bundle)
	}
	n, err := m.checkReplicas(ctx, t, r, cfg, classes, stateful)
	if err != nil {
		return resp, err
	}
	if n > 1 {
		// Dry placement first: a deploy that can't place every replica
		// writes nothing.
		ex := map[string]bool{}
		for i := range n {
			dec, _, err := m.place(ctx, t, placementInput{Workload: ReplicaName(r.Workload, i), Replace: r.Replace, Config: cfg, Exclude: ex})
			if err != nil {
				if e, ok := err.(*Error); ok && e.Code == "no_host" {
					ec := *e
					ec.Message = fmt.Sprintf("replicas: %d needs %d distinct live hosts (placed %d): %s", n, n, i, e.Message)
					return resp, &ec
				}
				return resp, err
			}
			ex[dec.Host] = true
		}
	}
	used := map[string]bool{}
	var names []string
	for i := range n {
		rr := r
		rr.Workload = ReplicaName(r.Workload, i)
		rr.replica = &replicaCtx{group: r.Workload, index: i, n: n, exclude: maps.Clone(used)}
		one, err := m.Deploy(ctx, rr)
		if err != nil {
			if e, ok := err.(*Error); ok && i > 0 {
				ec := *e
				ec.Message = fmt.Sprintf("replica %s: %s (replicas %s were deployed)", rr.Workload, e.Message, strings.Join(names, ", "))
				return resp, &ec
			}
			return resp, err
		}
		used[one.Host] = true
		names = append(names, rr.Workload)
		if i == 0 {
			resp = one
		}
		if n > 1 {
			resp.Replicas = append(resp.Replicas, ReplicaRef{Workload: rr.Workload, DeploymentID: one.DeploymentID, Host: one.Host})
		}
	}
	m.stopExtraReplicas(ctx, t.ID, r.Workload, n, r.By)
	return resp, nil
}

// replicaView lists a workload's replicas with their status (nil for a
// workload without replicas, and for a replica itself).
func (m *Mgmt) replicaView(ctx context.Context, d Deployment, status string) []ReplicaRef {
	if replicaGroup(d.Namespaces) != "" || d.StoppedAt != 0 {
		return nil
	}
	reps, err := m.replicasOf(ctx, d.TenantID, d.Workload)
	if err != nil || len(reps) == 0 {
		return nil
	}
	out := []ReplicaRef{{Workload: d.Workload, DeploymentID: d.ID, Host: d.HostID, Status: status}}
	for i := 1; i <= MaxReplicas; i++ {
		if rd, ok := reps[i]; ok {
			st, _ := m.deploymentStatus(ctx, rd)
			out = append(out, ReplicaRef{Workload: rd.Workload, DeploymentID: rd.ID, Host: rd.HostID, Status: st})
		}
	}
	return out
}

// ReplicaRef is one replica of a deploy (DeployResponse, DeploymentView).
type ReplicaRef struct {
	Workload     string `json:"workload"`
	DeploymentID string `json:"deploymentID"`
	Host         string `json:"host"`
	Status       string `json:"status,omitempty"`
}
