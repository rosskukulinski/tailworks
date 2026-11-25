package mgmt

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// tailworks.jsonc "replicas": a stateless workload on N distinct hosts behind one
// Service; each replica is its own workload (lease, record, assignment,
// node); scale-down and undeploy stop replicas; stateful bundles, too few
// hosts and name clashes are refused.
func TestWorkloadReplicas(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	hosts := map[string]*apiClient{}
	for _, id := range []string{"h1", "h2", "h3"} {
		hosts[id] = v1Host(t, e, id)
	}
	// The subtests run past HostLeaseTTL on a loaded runner (#541).
	keepHostsLive(t, e, "h1", "h2", "h3")
	c := e.c
	bundle := func(jsonc string) string {
		return base64.StdEncoding.EncodeToString(makeBundle(t, map[string]string{"tailworks.jsonc": jsonc, "dist/index.js": "export default {}"}))
	}
	web := func(n string) map[string]any {
		return map[string]any{"tenantID": tn, "workload": "web", "bundle": bundle(`{ "replicas": ` + n + `, "tailnet": { "tags": ["tag:tw-web"], "service": true } }`)}
	}
	// assigned maps workload → host from every host's assignments.
	assigned := func(t *testing.T) map[string]Assignment {
		t.Helper()
		out := map[string]Assignment{}
		for id, h := range hosts {
			var as []Assignment
			replicasClientFor(t, h).items("/v1/hosts/"+id+"/assignments", &as)
			for _, a := range as {
				if _, dup := out[a.Workload]; dup {
					t.Fatalf("workload %s assigned to two hosts", a.Workload)
				}
				a.Lease = id // stash the host
				out[a.Workload] = a
			}
		}
		return out
	}

	var dep DeploymentView
	t.Run("ThreeReplicasOnDistinctHosts", func(t *testing.T) {
		c := replicasClientFor(t, c)
		c.ok("POST", "/v1/deployments", web("3"), &dep)
		if len(dep.Replicas) != 3 {
			t.Fatalf("deployment replicas %+v", dep.Replicas)
		}
		as := assigned(t)
		seen := map[string]bool{}
		for i, name := range []string{"web", "web--r2", "web--r3"} {
			a, ok := as[name]
			if !ok {
				t.Fatalf("%s not assigned: %v", name, as)
			}
			if a.Service != "svc:web" || !slices.Equal(a.WorkloadTags, []string{"tag:tw-web"}) || a.Fence {
				t.Fatalf("%s assignment %+v", name, a)
			}
			if seen[a.Lease] {
				t.Fatalf("two replicas on host %s", a.Lease)
			}
			seen[a.Lease] = true
			if dep.Replicas[i].Workload != name || dep.Replicas[i].Host != a.Lease {
				t.Fatalf("replica %d = %+v, assigned on %s", i, dep.Replicas[i], a.Lease)
			}
		}
		if e.ts.svcPuts != 1 || e.ts.services["svc:web"]["comment"] != serviceOwner(tn, "web") {
			t.Fatalf("service: %d puts, %v", e.ts.svcPuts, e.ts.services["svc:web"])
		}
	})

	t.Run("RedeployIsSticky", func(t *testing.T) {
		c := replicasClientFor(t, c)
		before := assigned(t)
		var again DeploymentView
		c.ok("POST", "/v1/deployments", web("3"), &again)
		after := assigned(t)
		for _, name := range []string{"web", "web--r2", "web--r3"} {
			if before[name].Lease != after[name].Lease || before[name].DeploymentID == after[name].DeploymentID {
				t.Fatalf("%s: host %s → %s, deployment %s → %s", name, before[name].Lease, after[name].Lease, before[name].DeploymentID, after[name].DeploymentID)
			}
		}
	})

	t.Run("ReplicaNotDeployableByName", func(t *testing.T) {
		c := replicasClientFor(t, c)
		r := web("1")
		r["workload"] = "web--r2"
		c.fail("POST", "/v1/deployments", r, 409, "replica_conflict")
	})

	t.Run("ScaleDown", func(t *testing.T) {
		c := replicasClientFor(t, c)
		c.ok("POST", "/v1/deployments", web("2"), &dep)
		as := assigned(t)
		if _, ok := as["web--r3"]; ok {
			t.Fatal("web--r3 still assigned after scaling to 2")
		}
		if _, ok := as["web--r2"]; !ok || len(dep.Replicas) != 2 {
			t.Fatalf("after scale-down: %v, view %+v", as, dep.Replicas)
		}
	})

	t.Run("TooFewHosts", func(t *testing.T) {
		c := replicasClientFor(t, c)
		eb := c.fail("POST", "/v1/deployments", web("4"), 503, "no_host")
		if !strings.Contains(eb["message"].(string), "4 distinct live hosts") {
			t.Fatalf("message %v", eb["message"])
		}
		// Nothing changed: still two replicas.
		if as := assigned(t); len(as) != 2 {
			t.Fatalf("a refused deploy changed assignments: %v", as)
		}
	})

	t.Run("StatefulRefused", func(t *testing.T) {
		c := replicasClientFor(t, c)
		eb := c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "counter",
			"bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"], "objects": { "Counter": { "tags": ["tag:tw-web"] } } } }`)}, 400, "bad_bundle")
		if !strings.Contains(eb["message"].(string), "stateless") || !strings.Contains(eb["message"].(string), "Counter") {
			t.Fatalf("message %v", eb["message"])
		}
		// The dry run (tw deploy's preflight) refuses it the same way.
		c.fail("POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": tn, "workload": "counter",
			"bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"], "objects": { "Counter": { "tags": ["tag:tw-web"] } } } }`)}, 400, "bad_bundle")
		// "fence": "never" classes tolerate several instances.
		c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "cache",
			"bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"], "objects": { "Cache": { "tags": ["tag:tw-web"], "fence": "never" } } } }`)}, nil)
		c.ok("DELETE", "/v1/deployments/"+latestID(t, e, tn, "cache"), nil, nil)
	})

	t.Run("BadCounts", func(t *testing.T) {
		c := replicasClientFor(t, c)
		c.fail("POST", "/v1/deployments", web("17"), 400, "bad_bundle")
		c.fail("POST", "/v1/deployments", web("-1"), 400, "bad_bundle")
		r := web("2")
		r["host"] = "h1"
		c.fail("POST", "/v1/deployments", r, 400, "bad_request")
	})

	t.Run("NameTakenBySeparateWorkload", func(t *testing.T) {
		c := replicasClientFor(t, c)
		// A workload of its own can no longer be given a replica's name
		// (#348, TestRT_Hr2group_ReplicaNameReserved); one deployed before
		// that rule still blocks the replica that would take its name.
		c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "api--r2", "bundle": bundle(`{ "tailnet": { "tags": ["tag:tw-web"] } }`)}, 409, "replica_conflict")
		seedLegacyWorkload(t, e, tn, "api--r2", "h1", makeBundle(t, map[string]string{"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"] } }`, "dist/index.js": "export default {}"}))
		c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "api", "bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"] } }`)}, 409, "replica_conflict")
	})

	t.Run("UndeployStopsReplicas", func(t *testing.T) {
		c := replicasClientFor(t, c)
		c.ok("DELETE", "/v1/deployments/"+dep.DeploymentID, nil, nil)
		as := assigned(t)
		for _, name := range []string{"web", "web--r2"} {
			if _, ok := as[name]; ok {
				t.Fatalf("%s still assigned after undeploying web", name)
			}
		}
		// Back to a single copy: no replicas.
		var one DeploymentView
		c.ok("POST", "/v1/deployments", web("1"), &one)
		if len(one.Replicas) != 0 {
			t.Fatalf("replicas: 1 has replicas %+v", one.Replicas)
		}
		if _, ok := assigned(t)["web--r2"]; ok {
			t.Fatal("web--r2 came back with replicas: 1")
		}
	})
}

// replicasClientFor is c failing t, so a subtest's failure is its own
// (not a FailNow on the parent test).
func replicasClientFor(t *testing.T, c *apiClient) *apiClient {
	cc := *c
	cc.t = t
	return &cc
}

func latestID(t *testing.T, e *testEnv, tenant, workload string) string {
	t.Helper()
	ds, err := e.store.LatestDeployments(t.Context(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Workload == workload {
			return d.ID
		}
	}
	t.Fatalf("no deployment of %s", workload)
	return ""
}
