package mgmt

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/replicaname"
)

// TestRT_Hr2group_ReplicaNameReserved pins issue #348 (ledger H-r2group).
//
// hostd finds the workload whose R2 blobs a replica shares from the
// replica's name alone (hostd/r2.go r2Group, replicaname.Group), while the
// bucket's index, every other store and the secrets follow the deployment
// record (storeScope, replicaGroupOf). The two disagreed for one kind of
// name: a workload of its own called "web--r2" kept its index to itself and
// wrote its bytes under web's blob prefix. mgmt now refuses such a name to
// anything but a replica, so for every name a deploy accepts the two agree.
// Before the fix the first refusal below was a 202.
func TestRT_Hr2group_ReplicaNameReserved(t *testing.T) {
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	for _, id := range []string{"h1", "h2"} {
		v1Host(t, e, id)
	}
	c := e.c
	ctx := context.Background()
	rawBundle := func(jsonc string) []byte {
		return makeBundle(t, map[string]string{"tailworks.jsonc": jsonc, "dist/index.js": "export default {}"})
	}
	bundle := func(jsonc string) string { return base64.StdEncoding.EncodeToString(rawBundle(jsonc)) }
	const solo = `{ "tailnet": { "tags": ["tag:tw-web"] } }`
	deploy := func(name string) map[string]any {
		return map[string]any{"tenantID": tn, "workload": name, "bundle": bundle(solo)}
	}
	live := func() map[string]Deployment {
		t.Helper()
		ds, err := e.store.LatestDeployments(ctx, tn)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Deployment{}
		for _, d := range ds {
			if d.StoppedAt == 0 {
				out[d.Workload] = d
			}
		}
		return out
	}
	// agree: hostd's blob prefix for name is the workload mgmt scopes name's
	// stores to.
	agree := func(t *testing.T, name string) {
		t.Helper()
		blob := name
		if g, ok := replicaname.Group(name); ok {
			blob = g
		}
		scope, err := e.m.storeScope(ctx, tn, name, "r2", "bucket")
		if err != nil {
			t.Fatal(err)
		}
		if scope != blob {
			t.Fatalf("%s: mgmt scopes its stores to %q, hostd puts its R2 blobs under %q", name, scope, blob)
		}
	}

	t.Run("NewWorkloadRefused", func(t *testing.T) {
		for _, name := range []string{"web--r2", "web--r3", "web--r16", "web--r17", "web--r100", "a--r2--r3", "web--preview-x--r2"} {
			eb := c.fail("POST", "/v1/deployments", deploy(name), 409, "replica_conflict")
			g, _ := replicaname.Group(name)
			if msg, _ := eb["message"].(string); !strings.Contains(msg, `"`+g+`"`) || !strings.Contains(msg, "rename") {
				t.Errorf("%s: the refusal should name the workload it would be a replica of (%s) and say to rename: %v", name, g, eb["message"])
			}
			// tw deploy's preflight refuses it the same way.
			c.fail("POST", "/v1/deployments?dryRun=true", deploy(name), 409, "replica_conflict")
		}
		if l := live(); len(l) != 0 {
			t.Fatalf("a refused deploy wrote a deployment: %v", l)
		}
	})

	t.Run("OtherNamesStillDeploy", func(t *testing.T) {
		// Close to a replica's name, but not one: hostd strips nothing from
		// these either.
		for _, name := range []string{"web--r1", "web--r02", "web--r", "web--rx", "web-r2", "web--r2x", "r2"} {
			if _, ok := replicaname.Group(name); ok {
				t.Fatalf("%s is a replica name", name)
			}
			c.ok("POST", "/v1/deployments", deploy(name), nil)
			agree(t, name)
			c.ok("DELETE", "/v1/deployments/"+latestID(t, e, tn, name), nil, nil)
		}
	})

	t.Run("ReplicasStillDeploy", func(t *testing.T) {
		web := map[string]any{"tenantID": tn, "workload": "web", "bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"] } }`)}
		var dep DeploymentView
		c.ok("POST", "/v1/deployments", web, &dep)
		if len(dep.Replicas) != 2 || dep.Replicas[1].Workload != "web--r2" {
			t.Fatalf("replicas %+v", dep.Replicas)
		}
		c.ok("POST", "/v1/deployments", web, &dep) // and redeploy
		l := live()
		if replicaGroup(l["web--r2"].Namespaces) != "web" || replicaGroup(l["web"].Namespaces) != "" {
			t.Fatalf("deployments %+v", l)
		}
		// The property the rule protects, for the group and its replica.
		agree(t, "web")
		agree(t, "web--r2")
		// By its own name a replica is still refused, at the deploy and at
		// its dry run.
		c.fail("POST", "/v1/deployments", deploy("web--r2"), 409, "replica_conflict")
		c.fail("POST", "/v1/deployments?dryRun=true", deploy("web--r2"), 409, "replica_conflict")
		c.ok("DELETE", "/v1/deployments/"+dep.DeploymentID, nil, nil)
		// Once the replica is gone its name does not become free for a
		// workload of its own.
		c.fail("POST", "/v1/deployments", deploy("web--r2"), 409, "replica_conflict")
	})

	t.Run("PreviewAliasRefused", func(t *testing.T) {
		c.ok("POST", "/v1/deployments", deploy("shop"), nil)
		r := deploy("shop")
		r["previewAlias"] = "pr7--r2"
		eb := c.fail("POST", "/v1/deployments", r, 409, "replica_conflict")
		if msg, _ := eb["message"].(string); !strings.Contains(msg, "--preview-alias") {
			t.Errorf("the refusal should point at the alias: %v", eb["message"])
		}
		if _, ok := live()["shop--preview-pr7--r2"]; ok {
			t.Fatal("the refused preview was deployed")
		}
		r["previewAlias"] = "pr7"
		c.ok("POST", "/v1/deployments", r, nil)
		agree(t, "shop--preview-pr7")
	})

	t.Run("ExistingWorkloadKeepsDeploying", func(t *testing.T) {
		// A workload deployed under such a name before this rule is not
		// renamed: while it is live it redeploys. It goes straight into the
		// store, as the API no longer makes one.
		seedLegacyWorkload(t, e, tn, "old--r2", "h1", rawBundle(solo))
		c.ok("POST", "/v1/deployments", deploy("old--r2"), nil)
		c.ok("POST", "/v1/deployments?dryRun=true", deploy("old--r2"), nil)
		if d := live()["old--r2"]; d.ID == "" || replicaGroup(d.Namespaces) != "" {
			t.Fatalf("old--r2 after its redeploy: %+v", d)
		}
		// It is the one case left where the two disagree, as the docs say:
		// its index is its own, and hostd files its bytes under "old".
		scope, err := e.m.storeScope(ctx, tn, "old--r2", "r2", "bucket")
		if g, _ := replicaname.Group("old--r2"); err != nil || scope != "old--r2" || g != "old" {
			t.Fatalf("old--r2: store scope %q (%v), blob group %q", scope, err, g)
		}
		// Its replica-to-be is still told apart from it (the older check).
		c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "old", "bundle": bundle(`{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"] } }`)}, 409, "replica_conflict")
		// Undeployed, the name is gone for good.
		c.ok("DELETE", "/v1/deployments/"+latestID(t, e, tn, "old--r2"), nil, nil)
		c.fail("POST", "/v1/deployments", deploy("old--r2"), 409, "replica_conflict")
	})
}

// seedLegacyWorkload puts a live deployment of workload name on host into
// the store, as a mgmt from before #348 accepted it: a workload of its own
// with a replica's name. The API refuses to create one now.
func seedLegacyWorkload(t *testing.T, e *testEnv, tenant, name, host string, bundle []byte) {
	t.Helper()
	d := Deployment{ID: newID("dp_"), TenantID: tenant, Workload: name, HostID: host, Epoch: 1, Bundle: bundle,
		Namespaces: []ClassConfig{{Class: WorkloadNamespace, Tags: []string{"tag:tw-web"}}},
		MaxLive:    DefaultMaxLive, MaxDepth: DefaultMaxDepth, CreatedAt: time.Now().UnixMilli(), DeployedBy: "legacy"}
	if err := e.store.CreateDeployment(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}
