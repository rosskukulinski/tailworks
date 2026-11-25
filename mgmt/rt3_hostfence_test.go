package mgmt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestRT3_HostWritesAreLeaseBound (security/RED-TEAM.md Pass 3, RT3-01/02/
// 04/05/12; security/LEDGER.md H01, H02, H04, H05, H12): a registered host
// is trusted only for the leases it holds. Host h2, registered and
// heartbeating, must not be able to act on a workload whose lease h1 holds:
// create or spawn its objects, report them ready (and so pick their node
// IDs and get a node certificate), adopt h1's pool nodes, set h1's
// deployment status, or write audit records about its objects. h1's own
// calls keep working, with and without the epoch.
func TestRT3_HostWritesAreLeaseBound(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1, h2 := e.c, e.asHost("h1"), e.asHost("h2")
	ctx := context.Background()
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	pub1, key1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, key2, _ := ed25519.GenerateKey(rand.Reader)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}, "auditPubKey": base64.StdEncoding.EncodeToString(pub1)}, nil)
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}, "auditPubKey": base64.StdEncoding.EncodeToString(pub2)}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	var dep struct {
		DeploymentID string
		Host         string
		Epoch        int64
	}
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle, "host": "h1"}, &dep)
	if dep.Host != "h1" || dep.Epoch < 1 {
		t.Fatalf("deployment %+v", dep)
	}
	obj := func(name string, extra map[string]any) map[string]any {
		b := map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": "SupportAgent", "objectName": name}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// RT3-01: h2 cannot create on h1's lease: with no epoch, a wrong epoch,
	// or even the right epoch. The refusal is audited as a deny.
	h2.fail("POST", "/v1/objects", obj("victim", nil), 409, "lease_lost")
	h2.fail("POST", "/v1/objects", obj("victim", map[string]any{"epoch": dep.Epoch}), 409, "lease_lost")
	h2.fail("POST", "/v1/objects", obj("victim", map[string]any{"epoch": 99, "actingFor": "ceo@example.com"}), 409, "lease_lost")
	if objs, _ := e.store.ListObjects(ctx, ObjectFilter{TenantID: tn.TenantID}); len(objs) != 0 {
		t.Fatalf("h2's refused creates left objects: %+v", objs)
	}
	// h1 creates with its epoch (what hostd sends); a stale epoch is refused.
	var root CreateObjectResponse
	if st := h1.okStatus("POST", "/v1/objects", obj("root", map[string]any{"epoch": dep.Epoch}), &root); st != 202 {
		t.Fatalf("h1 create: %d", st)
	}
	h1.fail("POST", "/v1/objects", obj("stale", map[string]any{"epoch": dep.Epoch + 1}), 409, "lease_lost")
	// …and the spawn path is fenced the same way.
	h2.fail("POST", "/v1/objects", obj("child", map[string]any{"parent": map[string]any{"objectID": root.ObjectID}}), 409, "lease_lost")

	// RT3-02: h2 cannot report h1's object ready, with or without an epoch,
	// so it never chooses the node IDs or obtains a certificate.
	var cred Credential
	h1.ok("POST", "/v1/objects/"+root.ObjectID+"/credentials?epoch="+itoa(dep.Epoch)+"&wait=10", nil, &cred)
	h2.fail("PATCH", "/v1/objects/"+root.ObjectID, map[string]any{"status": "ready", "nodeID": "9", "stableNodeID": "nEVIL", "magicDNS": "evil.t.ts.net"}, 409, "lease_lost")
	h2.fail("PATCH", "/v1/objects/"+root.ObjectID, map[string]any{"status": "ready", "nodeID": "9", "stableNodeID": "nEVIL", "magicDNS": "evil.t.ts.net", "epoch": dep.Epoch}, 409, "lease_lost")
	if o, _ := e.store.GetObject(ctx, root.ObjectID); o.Status == StatusReady || o.StableNodeID != "" || o.Cert != "" {
		t.Fatalf("h2's refused ready changed the object: %+v", o)
	}
	h1.ok("PATCH", "/v1/objects/"+root.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": "nROOT", "magicDNS": root.ObjectID + ".t.ts.net", "epoch": dep.Epoch}, nil)
	if o, _ := e.store.GetObject(ctx, root.ObjectID); o.Status != StatusReady || o.StableNodeID != "nROOT" || o.Cert == "" {
		t.Fatalf("h1 ready: %+v", o)
	}

	// RT3-04: a pool node h1 joined cannot be adopted by h2's create (which is
	// refused anyway), and would not qualify even if the fence let it through.
	var pool CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "workload": "support", "namespace": PoolNamespace, "objectName": "SupportAgent/p1", "epoch": dep.Epoch}, &pool)
	h1.ok("POST", "/v1/objects/"+pool.ObjectID+"/credentials?epoch="+itoa(dep.Epoch)+"&wait=10", nil, &cred)
	h1.ok("PATCH", "/v1/objects/"+pool.ObjectID, map[string]any{"status": "ready", "nodeID": "2", "stableNodeID": "nPOOL", "magicDNS": pool.ObjectID + ".t.ts.net", "epoch": dep.Epoch}, nil)
	h2.fail("POST", "/v1/objects", obj("thief", map[string]any{"poolObjectID": pool.ObjectID}), 409, "lease_lost")
	if p, _ := e.store.GetObject(ctx, pool.ObjectID); p.Status != StatusReady || p.StableNodeID != "nPOOL" {
		t.Fatalf("pool node after h2's attempt: %+v", p)
	}
	dp, _ := e.store.GetDeployment(ctx, dep.DeploymentID)
	if e.m.poolJoinedBy(ctx, mustObj(t, e, pool.ObjectID), dp, "h2") || !e.m.poolJoinedBy(ctx, mustObj(t, e, pool.ObjectID), dp, "h1") {
		t.Fatal("poolJoinedBy: the pool node was joined by h1, not h2")
	}
	var a CreateObjectResponse
	h1.ok("POST", "/v1/objects", obj("alice", map[string]any{"poolObjectID": pool.ObjectID, "epoch": dep.Epoch}), &a)
	if a.Status != StatusAssigned {
		t.Fatalf("h1 adopting its own pool node: %+v", a)
	}

	// RT3-12: h2's heartbeat cannot set the status of h1's deployment.
	h2.ok("PUT", "/v1/hosts/h2/status", map[string]any{"liveNodes": 0, "leases": []any{},
		"deployments": []map[string]any{{"deploymentID": dep.DeploymentID, "status": "failed", "error": "pwned by h2"}}}, nil)
	var dv struct {
		Status string
		Error  string
	}
	c.ok("GET", "/v1/deployments/"+dep.DeploymentID, nil, &dv)
	if dv.Status == "failed" || dv.Error != "" {
		t.Fatalf("h2 set h1's deployment status: %+v", dv)
	}
	h1.ok("PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 2, "leases": []map[string]any{{"namespace": "support", "epoch": dep.Epoch}},
		"deployments": []map[string]any{{"deploymentID": dep.DeploymentID, "status": "active"}}}, nil)
	c.ok("GET", "/v1/deployments/"+dep.DeploymentID, nil, &dv)
	if dv.Status != "active" {
		t.Fatalf("h1 setting its own deployment status: %+v", dv)
	}

	// RT3-05: audit records must be signed by the registered key and may
	// only name objects of workloads the host has served.
	rec := func(key ed25519.PrivateKey, prev, objectID, actingFor string) map[string]any {
		r := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "objectID": objectID, "kind": "allow", "source": "host",
			"traceID": "tr-rt3", "actingFor": actingFor, "prevHash": prev}
		b, _ := json.Marshal(r)
		h, err := HashRecord(b)
		if err != nil {
			t.Fatal(err)
		}
		r["hash"] = h
		r["hostSig"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(h)))
		return r
	}
	h2.fail("POST", "/v1/audit", map[string]any{"records": []any{rec(key2, "", root.ObjectID, "ceo@example.com")}}, 403, "audit_object_foreign")
	h2.fail("POST", "/v1/audit", map[string]any{"records": []any{rec(key1, "", "", "ceo@example.com")}}, 400, "bad_host_sig")
	unsigned := rec(key2, "", "", "")
	delete(unsigned, "hostSig")
	h2.fail("POST", "/v1/audit", map[string]any{"records": []any{unsigned}}, 400, "bad_host_sig")
	var aud struct {
		Records []AuditRecord `json:"items"`
	}
	c.ok("GET", "/v1/audit?human=ceo@example.com", nil, &aud)
	if len(aud.Records) != 0 {
		t.Fatalf("forged attribution reached the audit log: %+v", aud.Records)
	}
	var resp struct {
		Accepted int
		Chain    ChainStatus
	}
	h1.ok("POST", "/v1/audit", map[string]any{"records": []any{rec(key1, "", root.ObjectID, "alice@example.com")}}, &resp)
	if resp.Accepted != 1 || !resp.Chain.OK {
		t.Fatalf("h1's own record: %+v", resp)
	}
	// A host registered without an audit key cannot write audit at all.
	h3 := e.asHost("h3")
	h3.ok("PUT", "/v1/hosts/h3", map[string]any{"capacityNodes": 1, "tiers": []string{"process"}}, nil)
	h3.fail("POST", "/v1/audit", map[string]any{"records": []any{rec(key2, "", "", "")}}, 403, "audit_key_required")
}

func mustObj(t *testing.T, e *testEnv, id string) Object {
	t.Helper()
	o, err := e.store.GetObject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
