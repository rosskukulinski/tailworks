package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestEstimateAppNodes: the estimate follows what mgmt mints nodes for
// (TwConfig.Classes): one workload node per copy, one public node per copy
// with public routes, one node per object of a class with tags, and none
// for a "node": false class.
func TestEstimateAppNodes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, cfg string
		preview   bool
		want      appNodes
		text      string
	}{
		{name: "workload only",
			cfg:  `{"tailnet": {"tags": ["tag:tw-wl-a"]}}`,
			want: appNodes{Workload: 1, Fixed: 1, PerObjectClasses: []string{}, SharedClasses: []string{}},
			text: "1 workload node"},
		{name: "classes",
			cfg: `{"tailnet": {"tags": ["tag:tw-wl-a"], "objects": {
				"Room": {"tags": ["tag:tw-room"]},
				"Agent": {"tags": ["tag:tw-agent"], "egress": "workload"},
				"Counter": {"node": false}}}}`,
			want: appNodes{Workload: 1, Fixed: 1, PerObjectClasses: []string{"Agent", "Room"}, SharedClasses: []string{"Counter"}},
			text: "1 workload node + 1 node per Agent object + 1 node per Room object (Counter objects share the workload node)"},
		{name: "replicas and public",
			cfg:  `{"replicas": 3, "public": {"routes": ["/hook"]}, "tailnet": {"tags": ["tag:tw-wl-a"]}}`,
			want: appNodes{Workload: 3, Public: 3, Fixed: 6, PerObjectClasses: []string{}, SharedClasses: []string{}},
			text: "3 workload nodes + 3 public nodes"},
		{name: "a preview is one copy without public routes",
			cfg:     `{"replicas": 3, "public": {"routes": ["/hook"]}, "tailnet": {"tags": ["tag:tw-wl-a"]}}`,
			preview: true,
			want:    appNodes{Workload: 1, Fixed: 1, PerObjectClasses: []string{}, SharedClasses: []string{}},
			text:    "1 workload node"},
	} {
		cfg, err := mgmt.ParseTwConfig([]byte(c.cfg))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := estimateAppNodes(cfg, c.preview)
		if fmt.Sprint(got) != fmt.Sprint(c.want) || got.Workload != c.want.Workload || got.Public != c.want.Public || got.Fixed != c.want.Fixed ||
			strings.Join(got.PerObjectClasses, ",") != strings.Join(c.want.PerObjectClasses, ",") || strings.Join(got.SharedClasses, ",") != strings.Join(c.want.SharedClasses, ",") {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.String() != c.text {
			t.Errorf("%s: text %q, want %q", c.name, got.String(), c.text)
		}
	}
	// No config: the workload node alone, and lists that print as [].
	if b, _ := json.Marshal(estimateAppNodes(nil, false)); string(b) != `{"workload":1,"public":0,"fixed":1,"perObjectClasses":[],"sharedClasses":[]}` {
		t.Errorf("nil config: %s", b)
	}
}

// TestCountNodes: live objects are nodes, sorted by namespace; terminal
// ones are not counted.
func TestCountNodes(t *testing.T) {
	t.Parallel()
	objs := []mgmt.Object{
		{Namespace: mgmt.WorkloadNamespace, Status: mgmt.StatusReady},
		{Namespace: mgmt.WorkloadNamespace, Status: mgmt.StatusReady},
		{Namespace: "Room", Status: mgmt.StatusReady},
		{Namespace: "Room", Status: mgmt.StatusPending},
		{Namespace: "Room", Status: mgmt.StatusKilled},
		{Namespace: "SubAgent", Status: mgmt.StatusMinted, ParentID: "x"},
		{Namespace: mgmt.PoolNamespace, Status: mgmt.StatusReady},
		{Namespace: mgmt.PoolNamespace, Status: mgmt.StatusTransferred},
		{Namespace: mgmt.PublicNamespace, Status: mgmt.StatusReady},
		{Namespace: "Room", Status: mgmt.StatusFailed},
	}
	n := countNodes(objs)
	if n != (tenantNodes{Total: 7, Workloads: 2, Objects: 3, Pool: 1, Public: 1}) {
		t.Fatalf("count: %+v", n)
	}
	if s := n.String(); s != "7 live nodes: 2 workloads, 3 objects, 1 pool, 1 public" {
		t.Errorf("text: %q", s)
	}
	if s := (tenantNodes{Total: 1, Workloads: 1}).String(); s != "1 live node: 1 workload" {
		t.Errorf("one: %q", s)
	}
	if s := (tenantNodes{}).String(); s != "0 live nodes" {
		t.Errorf("none: %q", s)
	}
}

// TestNodeWarnings: a warning at nodeBudgetWarnPct of the tenant's
// maxObjects, and of the hosts' node capacity (by the tenant's count or the
// hosts' own, whichever is higher).
func TestNodeWarnings(t *testing.T) {
	t.Parallel()
	if w := nodeWarnings("t.ts.net", nil, &nodeCapacity{Hosts: 1, CapacityNodes: 1}); len(w) != 0 || w == nil {
		t.Errorf("no count: %q (want an empty, non-nil list)", w)
	}
	if w := nodeWarnings("t.ts.net", &tenantNodes{Total: 79, MaxObjects: 100}, &nodeCapacity{Hosts: 1, CapacityNodes: 100, LiveNodes: 79}); len(w) != 0 {
		t.Errorf("under the margin: %q", w)
	}
	// No quota read, no capacity read (a linked developer machine): nothing to compare.
	if w := nodeWarnings("t.ts.net", &tenantNodes{Total: 5000}, nil); len(w) != 0 {
		t.Errorf("no limits known: %q", w)
	}
	w := nodeWarnings("t.ts.net", &tenantNodes{Total: 80, MaxObjects: 100}, &nodeCapacity{Hosts: 2, CapacityNodes: 1000, LiveNodes: 80})
	if len(w) != 1 || !strings.Contains(w[0], "80 live nodes, 80% of its maxObjects 100") || !strings.Contains(w[0], "object_quota_exceeded") || !strings.Contains(w[0], "tw tenant set t.ts.net --max-objects") {
		t.Errorf("near maxObjects: %q", w)
	}
	w = nodeWarnings("t.ts.net", &tenantNodes{Total: 45, MaxObjects: 1000}, &nodeCapacity{Hosts: 1, CapacityNodes: 50, LiveNodes: 41})
	if len(w) != 1 || !strings.Contains(w[0], "45 live nodes and its host runs 41 of 50 node capacity (90%; tw-hostd -capacity-nodes)") || !strings.Contains(w[0], "tw host add") {
		t.Errorf("near host capacity (tenant count): %q", w)
	}
	// Other tenants fill the hosts: the hosts' own count decides.
	w = nodeWarnings("t.ts.net", &tenantNodes{Total: 3, MaxObjects: 1000}, &nodeCapacity{Hosts: 2, CapacityNodes: 100, LiveNodes: 95})
	if len(w) != 1 || !strings.Contains(w[0], "3 live nodes and its hosts run 95 of 100 node capacity (95%") {
		t.Errorf("near host capacity (hosts' count): %q", w)
	}
	if w := nodeWarnings("t.ts.net", &tenantNodes{Total: 99, MaxObjects: 100}, &nodeCapacity{Hosts: 1, CapacityNodes: 100, LiveNodes: 99}); len(w) != 2 {
		t.Errorf("both: %q", w)
	}
}

func TestNodeBudgetFact(t *testing.T) {
	t.Parallel()
	if m := metaOf("node_budget"); m.group != "Tailnet" || m.fact == nil {
		t.Errorf("tw doctor's node_budget check has no group or fact: %+v", m)
	}
	if f := nodeBudgetFact("tenant t.ts.net has 12 live nodes: 1 workload, 11 objects (maxObjects 1000); every node …"); f != "12 live nodes" {
		t.Errorf("fact: %q", f)
	}
	if f := nodeBudgetFact("tenant a has 1 live node: 1 workload; tenant b has 2 live nodes: 2 workloads"); f != "" {
		t.Errorf("two tenants: %q", f)
	}
}

// TestCLINodeBudget: tw policy prints the estimate line with the tenant's
// live count, tw ls and tw doctor show the count and how many are objects,
// all carry it in --json, and each warns near the tenant's maxObjects.
func TestCLINodeBudget(t *testing.T) {
	e := newPubEnv(t) // public_test.go: a real tw-mgmt and a fake policy API
	ctx := context.Background()
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "chat",
  "tailnet": { "tags": ["tag:tw-wl-chat"], "objects": {
    "Room": { "tags": ["tag:tw-room"] },
    "Counter": { "node": false } } } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "chat", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [{"name": "ROOM", "class_name": "Room"}, {"name": "COUNTER", "class_name": "Counter"}]},
  "migrations": [{"tag": "v1", "new_sqlite_classes": ["Room", "Counter"]}]}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`import { DurableObject } from "cloudflare:workers";
export class Room extends DurableObject {}
export class Counter extends DurableObject {}
export default { fetch() { return new Response("ok"); } };`), 0o644)
	e.setPolicy(`{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:tw-wl-chat": ["tag:tw"], "tag:tw-room": ["tag:tw"]}}`)

	add := func(ns, name, status string) {
		t.Helper()
		id := strings.ToLower(strings.Trim(ns, "_")) + "-" + strings.ReplaceAll(name, "/", "-")
		if err := e.st.CreateObject(ctx, mgmt.Object{ID: id, TenantID: e.tenantID, Workload: "chat", Namespace: ns, Name: name, Hostname: id, RootID: id, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	add(mgmt.WorkloadNamespace, "chat", mgmt.StatusReady)
	add("Room", "lobby", mgmt.StatusReady)
	add("Room", "kitchen", mgmt.StatusPending)
	add("Room", "attic", mgmt.StatusKilled) // not a node any more
	add(mgmt.PoolNamespace, "Room/abc", mgmt.StatusReady)

	const est = "this app is 1 workload node + 1 node per Room object (Counter objects share the workload node)"
	const count = "tenant t.ts.net has 4 live nodes: 1 workload, 2 objects, 1 pool (maxObjects 1000"
	out, err := e.call(t, cmdPolicy, d)
	if err != nil {
		t.Fatalf("tw policy: %v\n%s", err, out)
	}
	t.Logf("tw policy:\n%s", out)
	for _, want := range []string{"  nodes      " + est + "; " + count, "your plan's device limit applies (docs/limits.md#tailnet-node-budget)"} {
		if !strings.Contains(out, want) {
			t.Errorf("tw policy lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "! nodes") {
		t.Errorf("tw policy warns far from any limit:\n%s", out)
	}
	out, err = e.call(t, cmdPolicy, d, "--json")
	if err != nil {
		t.Fatalf("tw policy --json: %v\n%s", err, out)
	}
	var pr struct {
		Nodes *nodeBudget `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Nodes == nil || pr.Nodes.App == nil || pr.Nodes.Tenant == nil {
		t.Fatalf("tw policy --json nodes: %v\n%s", err, out)
	}
	if a := pr.Nodes.App; a.Fixed != 1 || strings.Join(a.PerObjectClasses, ",") != "Room" || strings.Join(a.SharedClasses, ",") != "Counter" {
		t.Errorf("nodes.app: %+v", a)
	}
	if n := *pr.Nodes.Tenant; n != (tenantNodes{Total: 4, Workloads: 1, Objects: 2, Pool: 1, MaxObjects: 1000}) {
		t.Errorf("nodes.tenant: %+v", n)
	}
	if !strings.Contains(out, `"warnings": []`) {
		t.Errorf("nodes.warnings is not []:\n%s", out)
	}

	out, err = e.call(t, cmdLs)
	if err != nil || !strings.Contains(out, "NODES  4 live nodes: 1 workload, 2 objects, 1 pool (maxObjects 1000)") {
		t.Fatalf("tw ls: %v\n%s", err, out)
	}
	// --limit bounds the objects listed (and says so), never the count.
	out, err = e.call(t, cmdLs, "--limit", "1", "--json")
	var lr struct {
		Objects   []mgmt.Object `json:"objects"`
		Nodes     *nodeBudget   `json:"nodes"`
		Truncated bool          `json:"truncated"`
	}
	if err != nil || json.Unmarshal([]byte(out), &lr) != nil || lr.Nodes == nil || lr.Nodes.Tenant == nil || lr.Nodes.Tenant.Total != 4 || lr.Nodes.Tenant.Objects != 2 || len(lr.Objects) != 1 || !lr.Truncated || lr.Nodes.App != nil {
		t.Fatalf("tw ls --limit 1 --json: %v\n%s", err, out)
	}

	s, err := openState(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, dd, _ := doctorNodeBudget(s); st != "ok" || !strings.Contains(dd, count) || !strings.Contains(dd, "plan's device limit applies") {
		t.Fatalf("doctor node_budget: %s %s", st, dd)
	}

	// Near the tenant's quota: every surface warns.
	if _, err := e.call(t, cmdTenant, "set", e.tenantID, "--max-objects", "5"); err != nil {
		t.Fatal(err)
	}
	const warn = "tenant t.ts.net has 4 live nodes, 80% of its maxObjects 5"
	if out, err = e.call(t, cmdPolicy, d); err != nil || !strings.Contains(out, "! nodes      "+warn) {
		t.Errorf("tw policy near maxObjects: %v\n%s", err, out)
	}
	if out, err = e.call(t, cmdLs); err != nil || !strings.Contains(out, "  ! "+warn) {
		t.Errorf("tw ls near maxObjects: %v\n%s", err, out)
	}
	if st, dd, fix := doctorNodeBudget(s); st != "warn" || !strings.Contains(dd, warn) || fix == "" {
		t.Errorf("doctor node_budget near maxObjects: %s %s", st, dd)
	}
}
