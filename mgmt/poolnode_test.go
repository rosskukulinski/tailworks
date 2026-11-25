package mgmt

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPoolNodeAssignment (ADR-031 variant B): a "_pool" node joins like any
// object (mint, fenced credential, ready); a create that offers it gets it
// "assigned" with no mint, the pool row is "transferred" in the same
// transaction and both are audited; the object becomes ready only on that
// node; anything that doesn't match (class, state, already taken) falls
// back to a normal mint; concurrent offers of one node give it to one
// object; a kill deletes the device; transferred rows purge.
func TestPoolNodeAssignment(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, nil)
	ctx := context.Background()

	// join brings a pool node of class up to ready on node stable.
	join := func(class, suffix, stable string) string {
		t.Helper()
		var p CreateObjectResponse
		h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": PoolNamespace,
			"objectName": class + "/" + suffix}, &p)
		var cred Credential
		h1.ok("POST", "/v1/objects/"+p.ObjectID+"/credentials?epoch=1&wait=10", nil, &cred)
		h1.ok("PATCH", "/v1/objects/"+p.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": stable,
			"magicDNS": p.ObjectID + ".t.ts.net", "epoch": 1}, nil)
		return p.ObjectID
	}
	pool := join("SupportAgent", "p1", "nPOOL1")
	po, _ := e.store.GetObject(ctx, pool)
	if !strings.HasPrefix(pool, "pool-") || !slices.Equal(po.Tags, []string{"tag:tw-agent-support"}) || po.Ephemeral || po.TTLSeconds != 0 {
		t.Fatalf("pool node %s: %+v", pool, po)
	}
	h1.fail("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": PoolNamespace,
		"objectName": "no-slash"}, 400, "bad_request")

	keysBefore := func() int { e.ts.mu.Lock(); defer e.ts.mu.Unlock(); return len(e.ts.keys) }
	k0 := keysBefore()
	var a CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "SupportAgent",
		"objectName": "alice", "poolObjectID": pool}, &a)
	if a.Status != StatusAssigned {
		t.Fatalf("create with a usable pool node: status %q, want assigned", a.Status)
	}
	time.Sleep(200 * time.Millisecond)
	if n := keysBefore(); n != k0 {
		t.Fatalf("%d keys minted for an assigned object", n-k0)
	}
	ao, _ := e.store.GetObject(ctx, a.ObjectID)
	po, _ = e.store.GetObject(ctx, pool)
	if ao.StableNodeID != "nPOOL1" || ao.KeyID != "" || po.Status != StatusTransferred || po.StableNodeID != "" || po.Cert != "" || po.Live() {
		t.Fatalf("after transfer: object %+v\npool %+v", ao, po)
	}
	h1.fail("POST", "/v1/objects/"+a.ObjectID+"/credentials?epoch=1", nil, 409, "already_joined")
	// Ready only on the assigned node, and fenced as before.
	h1.fail("PATCH", "/v1/objects/"+a.ObjectID, map[string]any{"status": "ready", "nodeID": "2", "stableNodeID": "nOTHER", "magicDNS": "x", "epoch": 1}, 409, "identity_conflict")
	e.asHost("h2").fail("PATCH", "/v1/objects/"+a.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": "nPOOL1", "magicDNS": a.ObjectID + ".t.ts.net", "epoch": 1}, 409, "lease_lost")
	h1.ok("PATCH", "/v1/objects/"+a.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": "nPOOL1", "magicDNS": a.ObjectID + ".t.ts.net", "epoch": 1}, nil)
	ao, _ = e.store.GetObject(ctx, a.ObjectID)
	if ao.Status != StatusReady || ao.Cert == "" || ao.MagicDNS != a.ObjectID+".t.ts.net" {
		t.Fatalf("after ready: %+v", ao)
	}
	if objs, _ := e.store.ListObjects(ctx, ObjectFilter{StableNodeID: "nPOOL1"}); len(objs) != 1 || objs[0].ID != a.ObjectID {
		t.Fatalf("the device is named by %d rows, want only the object's", len(objs))
	}

	kinds := func(id string) []string {
		var aud struct {
			Items []AuditRecord `json:"items"`
		}
		c.ok("GET", "/v1/audit?object="+id, nil, &aud)
		var k []string
		for _, r := range aud.Items {
			k = append(k, r.Kind)
			if r.Kind == "create" && id == a.ObjectID {
				var d map[string]any
				json.Unmarshal(r.Detail, &d)
				if d["fromPool"] != pool || d["stableNodeID"] != "nPOOL1" {
					t.Errorf("object create record %s", r.Detail)
				}
			}
		}
		return k
	}
	// The wrong host's fenced ready attempt above is audited as a deny
	// (lease_lost): a cross-host ready is a security event, not noise.
	if k := kinds(a.ObjectID); strings.Join(k, ",") != "create,deny,ready" {
		t.Errorf("object audit %v, want create,deny,ready", k)
	}
	if k := kinds(pool); strings.Join(k, ",") != "create,mint,credential,ready,transfer" {
		t.Errorf("pool node audit %v", k)
	}

	// Not usable → a normal mint: a transferred node, another class's node.
	var b CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "SupportAgent",
		"objectName": "bob", "poolObjectID": pool}, &b)
	rogue := join("Rogue", "r1", "nROGUE")
	var b2 CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "SupportAgent",
		"objectName": "bob2", "poolObjectID": rogue}, &b2)
	if b.Status != StatusPending || b2.Status != StatusPending {
		t.Fatalf("unusable pool nodes: %q %q, want pending (normal mint)", b.Status, b2.Status)
	}
	if ro, _ := e.store.GetObject(ctx, rogue); ro.Status != StatusReady {
		t.Fatalf("another class's pool node was consumed: %s", ro.Status)
	}

	// Two creates offering the same node at once: one gets it.
	p2 := join("SupportAgent", "p2", "nPOOL2")
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	for _, name := range []string{"carol", "dave"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r CreateObjectResponse
			h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "SupportAgent",
				"objectName": name, "poolObjectID": p2}, &r)
			mu.Lock()
			got[r.Status]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if got[StatusAssigned] != 1 {
		t.Fatalf("concurrent offers of one pool node: %v, want exactly one assigned", got)
	}

	// Kill: the assigned object's device (the pool node's) is deleted.
	c.ok("DELETE", "/v1/objects/"+a.ObjectID+killQuery(false, "test"), nil, nil)
	for dl := time.Now().Add(5 * time.Second); !slices.Contains(e.ts.devicesDeleted(), "nPOOL1"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(dl) {
			t.Fatalf("device nPOOL1 not deleted after the kill: %v", e.ts.devicesDeleted())
		}
	}
	var pr PurgeResult
	c.ok("DELETE", "/v1/tenants/"+tn.TenantID+"/objects", nil, &pr)
	if !slices.Contains(pr.Purged, pool) || !slices.Contains(pr.Purged, a.ObjectID) {
		t.Fatalf("purge %v: want the transferred pool row and the killed object", pr.Purged)
	}
}
