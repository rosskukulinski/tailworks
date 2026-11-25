package mgmt

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestStalePoolNodesKilledOnMove (#567, found by the e2e/dataplane
// containers loop): pool nodes live in one host's state directory, so when
// a deploy moves the workload to another host the old host's pool nodes are
// killed and their devices deleted, instead of staying ready in mgmt with
// no host to run them. A redeploy on the same host keeps them, and the new
// host's own pool nodes are untouched.
func TestStalePoolNodesKilledOnMove(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1, h2 := e.c, e.asHost("h1"), e.asHost("h2")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	deploy := func(host string) int64 {
		t.Helper()
		var d struct{ Epoch int64 }
		c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle, "host": host, "replace": true, "forceTakeover": true}, &d)
		return d.Epoch
	}
	ctx := context.Background()
	join := func(h *apiClient, epoch int64, suffix, stable string) string {
		t.Helper()
		var p CreateObjectResponse
		h.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": PoolNamespace,
			"objectName": "SupportAgent/" + suffix}, &p)
		var cred Credential
		h.ok("POST", "/v1/objects/"+p.ObjectID+"/credentials?epoch="+itoa(epoch)+"&wait=10", nil, &cred)
		h.ok("PATCH", "/v1/objects/"+p.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": stable,
			"magicDNS": p.ObjectID + ".t.ts.net", "epoch": epoch}, nil)
		return p.ObjectID
	}
	status := func(id string) string {
		o, err := e.store.GetObject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return o.Status
	}
	waitStatus := func(id, want string) {
		t.Helper()
		for dl := time.Now().Add(5 * time.Second); status(id) != want; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(dl) {
				t.Fatalf("%s: %s, want %s", id, status(id), want)
			}
		}
	}

	ep := deploy("h1")
	old := join(h1, ep, "a1", "nOLD1")
	// A redeploy on the same host keeps its pool nodes.
	deploy("h1")
	time.Sleep(300 * time.Millisecond)
	if s := status(old); s != StatusReady {
		t.Fatalf("redeploy on the same host: pool node %s, want ready", s)
	}
	// The move to h2: h1's pool node is killed and its device deleted.
	ep = deploy("h2")
	waitStatus(old, StatusKilled)
	for dl := time.Now().Add(5 * time.Second); !slices.Contains(e.ts.devicesDeleted(), "nOLD1"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatalf("device nOLD1 not deleted after the move: %v", e.ts.devicesDeleted())
		}
	}
	// h2's own pool node survives a later redeploy on h2.
	mine := join(h2, ep, "b1", "nNEW1")
	deploy("h2")
	time.Sleep(300 * time.Millisecond)
	if s := status(mine); s != StatusReady {
		t.Fatalf("the holder's own pool node: %s, want ready", s)
	}
}
