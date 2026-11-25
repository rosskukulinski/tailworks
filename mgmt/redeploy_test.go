package mgmt

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestRedeployAfterUndeploy (issue #364): `tw undeploy demo` then `tw
// deploy` of the same app must work. Undeploy kills the workload node, and
// its deterministic ID made the next deploy's create answer 410 (the CLI's
// "bootstrap_failed: the workload node for demo is killed") until
// `tw down --purge`. A deploy after an undeploy now forgets the workload's
// killed objects first; while the workload stays deployed, a kill stays.
func TestRedeployAfterUndeploy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 10, "tiers": []string{"process"}}, nil)
	deploy := func(workload string) DeployResponse {
		t.Helper()
		var dep DeployResponse
		c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": workload, "bundle": noConfigBundle(t),
			"namespaces": []ClassConfig{{Class: WorkloadNamespace, Tags: []string{"tag:ttest"}}, {Class: "A", Tags: []string{"tag:ttest"}}}}, &dep)
		return dep
	}
	create := func(workload, ns, name string, want int) CreateObjectResponse {
		t.Helper()
		var r CreateObjectResponse
		body := map[string]any{"tenantID": tn.TenantID, "workload": workload, "namespace": ns, "objectName": name}
		if want == 410 {
			h1.fail("POST", "/v1/objects", body, 410, "killed")
			return r
		}
		if st := h1.okStatus("POST", "/v1/objects", body, &r); st != want {
			t.Fatalf("create %s %s/%s: %d, want %d", workload, ns, name, st, want)
		}
		return r
	}

	dep := deploy("demo")
	wl := create("demo", WorkloadNamespace, "demo", 202)
	room := create("demo", "A", "room", 202)
	h1.ok("PATCH", "/v1/objects/"+wl.ObjectID, map[string]any{"status": "ready", "epoch": dep.Epoch, "stableNodeID": "nWL", "magicDNS": "demo.t.ts.net"}, nil)
	// An explicit kill while deployed stays across a plain redeploy.
	gone := create("demo", "A", "gone", 202)
	c.ok("DELETE", "/v1/objects/"+gone.ObjectID+killQuery(false, "prompt injection"), nil, nil)
	dep = deploy("demo")
	create("demo", "A", "gone", 410)
	// Another workload's killed object is not this workload's to release.
	deploy("other")
	ko := create("other", "A", "x", 202)
	c.ok("DELETE", "/v1/objects/"+ko.ObjectID+killQuery(false, "test"), nil, nil)

	// Undeploy kills every object of demo, the workload node included.
	var kr KillResult
	c.ok("DELETE", "/v1/deployments/"+dep.DeploymentID, nil, &kr)
	if !slices.Contains(kr.Killed, wl.ObjectID) || !slices.Contains(kr.Killed, room.ObjectID) {
		t.Fatalf("undeploy killed %v", kr.Killed)
	}

	// The bug: the same app deployed again could not get its workload node.
	dep2 := deploy("demo")
	wl2 := create("demo", WorkloadNamespace, "demo", 202)
	if wl2.ObjectID != wl.ObjectID || wl2.Status == StatusKilled {
		t.Fatalf("workload node after redeploy: %+v (was %s)", wl2, wl.ObjectID)
	}
	var dv DeploymentView
	c.ok("GET", "/v1/deployments/"+dep2.DeploymentID, nil, &dv)
	if dv.Endpoint.Status == StatusKilled || dv.Endpoint.Status == StatusFailed {
		t.Fatalf("endpoint after redeploy: %+v", dv.Endpoint)
	}
	// Every name demo had can be created again, the explicitly killed one too.
	create("demo", "A", "room", 202)
	create("demo", "A", "gone", 202)
	// Their kill revocations went with them (else the host kills the new objects).
	if revs, err := e.m.store.Revocations(ctx, "h1", time.Time{}); err != nil || slices.Contains(revs, wl.ObjectID) || slices.Contains(revs, room.ObjectID) {
		t.Fatalf("revocations after redeploy: %v %v", revs, err)
	}
	// other is still deployed: its kill stands.
	create("other", "A", "x", 410)
	// Each release is audited as a purge.
	var recs []AuditRecord
	c.items("/v1/audit?tenant="+tn.TenantID+"&kind=purge", &recs)
	var purged []string
	for _, r := range recs {
		purged = append(purged, r.ObjectID)
	}
	for _, id := range []string{wl.ObjectID, room.ObjectID, gone.ObjectID} {
		if !slices.Contains(purged, id) {
			t.Fatalf("purge audit %v lacks %s", purged, id)
		}
	}
	if slices.Contains(purged, ko.ObjectID) {
		t.Fatalf("purge audit %v has other's object %s", purged, ko.ObjectID)
	}
}
