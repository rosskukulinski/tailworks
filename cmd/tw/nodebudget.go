package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// Tailnet node budget (#321; docs/limits.md "Tailnet node budget"). Every
// node tailworks runs for a tenant is an mgmt object and a device on the
// tenant's tailnet, so the tailnet plan's device limit applies to it. This
// file has:
//
//   - the estimate `tw policy` and `tw deploy`'s preflight print: what the
//     app's tailworks.jsonc costs in nodes (estimateAppNodes);
//   - the tenant's live node count, by what the nodes are (countNodes),
//     which `tw ls` and `tw doctor` also show;
//   - the warning when that count is close to a limit tailworks itself has:
//     the tenant's maxObjects, or the node capacity its hosts advertise
//     (tw-hostd -capacity-nodes).
//
// Nothing here adds API surface: the count is GET /v1/objects, the quota
// GET /v1/tenants/{id} and the capacity GET /v1/hosts (admin only: a linked
// developer machine gets the count and the quota, not the capacity).

// nodeBudgetWarnPct: the tenant's node count this close to its maxObjects,
// or to its hosts' node capacity, is a warning.
const nodeBudgetWarnPct = 80

// nodeBudgetDoc is where the counting rule is written down.
const nodeBudgetDoc = "docs/limits.md#tailnet-node-budget"

// appNodes is what one app costs in tailnet nodes, from its tailworks.jsonc.
type appNodes struct {
	// Workload nodes: one per copy of the workload ("replicas", default 1).
	Workload int `json:"workload"`
	// Public nodes: one per copy when the app has "public" routes.
	Public int `json:"public"`
	// Fixed is Workload + Public: the nodes the deploy itself joins.
	Fixed int `json:"fixed"`
	// PerObjectClasses get one node per object (tailnet.objects with tags).
	PerObjectClasses []string `json:"perObjectClasses"`
	// SharedClasses are internal ("node": false): their objects use the
	// workload's node and add none.
	SharedClasses []string `json:"sharedClasses"`
}

// estimateAppNodes reads the cost off the config, as mgmt will
// (TwConfig.Classes: the namespaces it mints nodes for). A preview is one
// copy with no public routes (mgmt/preview.go strips both).
func estimateAppNodes(cfg *mgmt.TwConfig, preview bool) appNodes {
	n := appNodes{Workload: 1, PerObjectClasses: []string{}, SharedClasses: []string{}}
	if cfg == nil {
		n.Fixed = n.Workload
		return n
	}
	if cfg.Replicas > 1 && !preview {
		n.Workload = cfg.Replicas
	}
	if cfg.Public != nil && !preview {
		n.Public = n.Workload
	}
	n.Fixed = n.Workload + n.Public
	for _, c := range cfg.Classes() {
		if c.Class != mgmt.WorkloadNamespace {
			n.PerObjectClasses = append(n.PerObjectClasses, c.Class)
		}
	}
	for name, o := range cfg.Tailnet.Objects {
		if o.Node != nil && !*o.Node {
			n.SharedClasses = append(n.SharedClasses, name)
		}
	}
	sort.Strings(n.SharedClasses)
	return n
}

// String is the estimate in words: "1 workload node + 1 node per Room
// object (Counter objects share the workload node)".
func (n appNodes) String() string {
	parts := []string{plural(n.Workload, "workload node")}
	if n.Public > 0 {
		parts = append(parts, plural(n.Public, "public node"))
	}
	for _, c := range n.PerObjectClasses {
		parts = append(parts, "1 node per "+c+" object")
	}
	s := strings.Join(parts, " + ")
	if len(n.SharedClasses) > 0 {
		s += " (" + strings.Join(n.SharedClasses, ", ") + " objects share the workload node)"
	}
	return s
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// tenantNodes is a tenant's live nodes, by what they are. Every one is a
// live mgmt object (pending, minted, assigned or ready), which is also
// what counts against the tenant's maxObjects.
type tenantNodes struct {
	Total int `json:"total"`
	// Workloads: workload entry nodes (one per deployed workload, replica
	// and preview).
	Workloads int `json:"workloads"`
	// Objects: Durable Object instances with a node of their own,
	// sub-agents included.
	Objects int `json:"objects"`
	// Pool: pre-joined pool nodes (ADR-031), waiting for a new object.
	Pool int `json:"pool"`
	// Public: public-ingress nodes.
	Public int `json:"public"`
	// MaxObjects is the tenant's quota of live objects (0: not read).
	MaxObjects int `json:"maxObjects,omitempty"`
}

// countNodes sorts live objects by namespace.
func countNodes(objs []mgmt.Object) tenantNodes {
	var n tenantNodes
	for _, o := range objs {
		if !o.Live() {
			continue
		}
		n.Total++
		switch o.Namespace {
		case mgmt.WorkloadNamespace:
			n.Workloads++
		case mgmt.PoolNamespace:
			n.Pool++
		case mgmt.PublicNamespace:
			n.Public++
		default:
			n.Objects++
		}
	}
	return n
}

// String: "12 live nodes: 1 workload, 10 objects, 1 pool".
func (n tenantNodes) String() string {
	if n.Total == 0 {
		return "0 live nodes"
	}
	var parts []string
	for _, p := range []struct {
		n        int
		one, two string
	}{
		{n.Workloads, "workload", "workloads"},
		{n.Objects, "object", "objects"},
		{n.Pool, "pool", "pool"},
		{n.Public, "public", "public"},
	} {
		switch {
		case p.n == 1:
			parts = append(parts, "1 "+p.one)
		case p.n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.two))
		}
	}
	return plural(n.Total, "live node") + ": " + strings.Join(parts, ", ")
}

// nodeCapacity is the node capacity the live hosts advertise
// (tw-hostd -capacity-nodes; placement's free capacity is capacityNodes −
// liveNodes) and the nodes they run, for every tenant they serve.
type nodeCapacity struct {
	Hosts         int `json:"hosts"`
	CapacityNodes int `json:"capacityNodes"`
	LiveNodes     int `json:"liveNodes"`
}

// nodeBudget is the "nodes" field of tw policy, tw deploy and tw ls --json.
type nodeBudget struct {
	// App: the app's estimate (tw policy and tw deploy only).
	App *appNodes `json:"app,omitempty"`
	// Tenant: absent when mgmt couldn't be asked.
	Tenant *tenantNodes `json:"tenant,omitempty"`
	// Capacity: absent when the caller may not list hosts (not an admin)
	// or no host is live.
	Capacity *nodeCapacity `json:"capacity,omitempty"`
	// Warnings: the count is within nodeBudgetWarnPct of a limit.
	Warnings []string `json:"warnings"`
}

// nodeWarnings lists the limits the tenant's node count is close to.
func nodeWarnings(tenant string, tn *tenantNodes, hc *nodeCapacity) []string {
	w := []string{}
	if tn == nil {
		return w
	}
	near := func(used, limit int) bool { return limit > 0 && used*100 >= limit*nodeBudgetWarnPct }
	if near(tn.Total, tn.MaxObjects) {
		w = append(w, fmt.Sprintf("tenant %s has %d live nodes, %d%% of its maxObjects %d: at %d new objects are refused (object_quota_exceeded); kill idle objects, or tw tenant set %s --max-objects N",
			tenant, tn.Total, 100*tn.Total/tn.MaxObjects, tn.MaxObjects, tn.MaxObjects, tenant))
	}
	if hc != nil {
		// The hosts' own count covers every tenant they serve; the tenant's
		// count also has nodes that are still joining.
		used := max(tn.Total, hc.LiveNodes)
		if near(used, hc.CapacityNodes) {
			w = append(w, fmt.Sprintf("tenant %s has %d live nodes and its %s %d of %d node capacity (%d%%; tw-hostd -capacity-nodes): a full host takes no new workloads; add a host (tw host add), or raise -capacity-nodes if the host has the memory",
				tenant, tn.Total, map[bool]string{true: "host runs", false: "hosts run"}[hc.Hosts == 1], hc.LiveNodes, hc.CapacityNodes, 100*used/hc.CapacityNodes))
		}
	}
	return w
}

// tenantNodeCount reads the tenant's live nodes and its maxObjects (nil if
// mgmt can't say).
func (s *state) tenantNodeCount(ctx context.Context, tenantID string) *tenantNodes {
	q := url.Values{"tenant": {tenantID}, "state": mgmt.LiveStatuses}
	objs, err := listAll[mgmt.Object](ctx, s.mgmt(), "/v1/objects?"+q.Encode(), 0)
	if err != nil {
		return nil
	}
	n := countNodes(objs)
	var t struct {
		MaxObjects int `json:"maxObjects"`
	}
	if s.mgmt().Do(ctx, "GET", "/v1/tenants/"+url.PathEscape(tenantID), nil, &t) == nil {
		n.MaxObjects = t.MaxObjects
	}
	return &n
}

// hostNodeCapacity sums the live hosts' node capacity (nil when the caller
// may not list hosts, or none is live).
func (s *state) hostNodeCapacity(ctx context.Context) *nodeCapacity {
	type hostRow struct {
		Status        string `json:"status"`
		CapacityNodes int    `json:"capacityNodes"`
		LiveNodes     int    `json:"liveNodes"`
	}
	hosts, err := listAll[hostRow](ctx, s.mgmt(), "/v1/hosts", 0)
	if err != nil {
		return nil
	}
	var c nodeCapacity
	for _, h := range hosts {
		if h.Status != "live" {
			continue
		}
		c.Hosts++
		c.CapacityNodes += h.CapacityNodes
		c.LiveNodes += h.LiveNodes
	}
	if c.Hosts == 0 {
		return nil
	}
	return &c
}

// nodeBudgetOf is the budget of tenant tn: its live nodes, the hosts'
// capacity and the warnings; with cfg, also the app's estimate. It never
// fails a command: what mgmt can't say is left out.
func (s *state) nodeBudgetOf(ctx context.Context, tn TenantRef, cfg *mgmt.TwConfig, preview bool) *nodeBudget {
	b := &nodeBudget{}
	if cfg != nil {
		e := estimateAppNodes(cfg, preview)
		b.App = &e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if b.Tenant = s.tenantNodeCount(ctx, tn.TenantID); b.Tenant != nil {
		b.Capacity = s.hostNodeCapacity(ctx)
	}
	b.Warnings = nodeWarnings(tn.Tailnet, b.Tenant, b.Capacity)
	return b
}

// summary is the tenant half of the estimate line: "tenant t.ts.net has 12
// live nodes: … (maxObjects 1000; hosts run 12 of 500 node capacity)".
func (b *nodeBudget) summary(tenant string) string {
	if b == nil || b.Tenant == nil {
		return ""
	}
	s := "tenant " + tenant + " has " + b.Tenant.String()
	var lim []string
	if b.Tenant.MaxObjects > 0 {
		lim = append(lim, fmt.Sprintf("maxObjects %d", b.Tenant.MaxObjects))
	}
	if c := b.Capacity; c != nil {
		lim = append(lim, fmt.Sprintf("%s %d of %d node capacity", map[bool]string{true: "host runs", false: "hosts run"}[c.Hosts == 1], c.LiveNodes, c.CapacityNodes))
	}
	if len(lim) > 0 {
		s += " (" + strings.Join(lim, "; ") + ")"
	}
	return s
}

// printNodeBudget is the estimate line of tw policy (prefix "  nodes     ")
// and of tw deploy's preflight ("✓ preflight  nodes: "), then one line per
// warning.
func printNodeBudget(prefix, warnPrefix, tenant string, b *nodeBudget) {
	if b == nil || b.App == nil {
		return
	}
	line := "this app is " + b.App.String()
	if s := b.summary(tenant); s != "" {
		line += "; " + s
	}
	fmt.Printf("%s%s\n", prefix, tsafe(line))
	for _, w := range b.Warnings {
		fmt.Printf("%s%s\n", warnPrefix, tsafe(w))
	}
	if len(b.Warnings) > 0 || len(b.App.PerObjectClasses) > 0 {
		fmt.Printf("%severy node is a device on your tailnet, so your plan's device limit applies (%s)\n", strings.Repeat(" ", len([]rune(warnPrefix))), nodeBudgetDoc)
	}
}

// printLsNodes is tw ls's NODES line, under TENANT.
func printLsNodes(b *nodeBudget) {
	if b == nil || b.Tenant == nil {
		return
	}
	line := b.Tenant.String()
	if b.Tenant.MaxObjects > 0 {
		line += fmt.Sprintf(" (maxObjects %d)", b.Tenant.MaxObjects)
	}
	fmt.Printf("NODES  %s\n", line)
	for _, w := range b.Warnings {
		fmt.Printf("  ! %s\n", tsafe(w))
	}
}

var reNodeBudgetFact = regexp.MustCompile(`has (\d+ live nodes?)`)

// nodeBudgetFact is what tw doctor's summary shows of a passing check: the
// count, when there is one tenant.
func nodeBudgetFact(detail string) string {
	if m := reNodeBudgetFact.FindAllStringSubmatch(detail, 2); len(m) == 1 {
		return m[0][1]
	}
	return ""
}

// doctorNodeBudget is tw doctor's node_budget check: every tenant's live
// nodes and how many are objects, and a warning near a limit.
func doctorNodeBudget(s *state) (string, string, string) {
	switch {
	case s == nil || (!s.configured() && !s.remote()):
		return "skip", "tw up not run here", ""
	case !s.remote() && !s.supervisorAlive():
		return "skip", "tw is not running here", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var parts, warns []string
	for _, t := range s.tenants() {
		b := s.nodeBudgetOf(ctx, t, nil, false)
		if b.Tenant == nil {
			continue
		}
		parts = append(parts, b.summary(t.Tailnet))
		warns = append(warns, b.Warnings...)
	}
	if len(parts) == 0 {
		return "skip", "mgmt reported no tenant's objects", ""
	}
	note := "; every node is a device on the tailnet, so the plan's device limit applies (" + nodeBudgetDoc + ")"
	if len(warns) > 0 {
		return "warn", strings.Join(warns, "; ") + note, "kill idle objects (tw ls, tw kill), give fewer classes their own node (\"node\": false), raise the quota (tw tenant set <tenant> --max-objects N) or add a host (tw host add)"
	}
	return "ok", strings.Join(parts, "; ") + note, ""
}
