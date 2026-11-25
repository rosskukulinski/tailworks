package mgmt

import (
	"testing"
)

// Issue #512: GET /v1/objects?tag=&human= and GET /v1/audit?workload= are
// filtered by mgmt, and a tenant admin still sees only its granted tenants.
func TestListFiltersTagHumanWorkload(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// A second object in tenant A, acting for a human.
	var o map[string]any
	if st, eb := e.call(nodeH1, "POST", "/v1/objects", map[string]any{"tenantID": e.tn, "workload": "w", "namespace": "A", "objectName": "b", "actingFor": "pat@example.com"}, &o); st != 202 {
		t.Fatalf("create b: %d %v", st, eb)
	}
	objB := o["objectID"].(string)

	ids := func(who, path string) []string {
		t.Helper()
		var page struct{ Items []map[string]any }
		if st, eb := e.call(who, "GET", path, nil, &page); st != 200 {
			t.Fatalf("%s: %d %v", path, st, eb)
		}
		var out []string
		for _, it := range page.Items {
			if id, ok := it["objectID"].(string); ok {
				out = append(out, id)
			}
		}
		return out
	}
	var all struct{ Items []map[string]any }
	e.c.ok("GET", "/v1/objects/"+e.objA, nil, nil)
	e.c.ok("GET", "/v1/objects", nil, &all)
	tags, _ := all.Items[0]["tags"].([]any)
	if len(tags) == 0 {
		t.Fatalf("object has no tags: %v", all.Items[0])
	}
	tag := tags[0].(string)

	// Admin: the tag is on every object of both tenants; a prefix of it is not a match.
	if got := ids(nodeAdm, "/v1/objects?tag="+tag); len(got) != 3 {
		t.Fatalf("tag %s: %v, want 3 objects", tag, got)
	}
	if got := ids(nodeAdm, "/v1/objects?tag="+tag[:len(tag)-1]); len(got) != 0 {
		t.Fatalf("tag prefix matched: %v", got)
	}
	if got := ids(nodeAdm, "/v1/objects?human=pat@example.com"); len(got) != 1 || got[0] != objB {
		t.Fatalf("human: %v, want [%s]", got, objB)
	}
	if got := ids(nodeAdm, "/v1/objects?human=pat@example"); len(got) != 0 {
		t.Fatalf("human is an exact match: %v", got)
	}
	if got := ids(nodeAdm, "/v1/objects?tag="+tag+"&human=pat@example.com"); len(got) != 1 {
		t.Fatalf("tag+human: %v", got)
	}
	// Tenant admin of tenant A: the shared tag lists only tenant A's objects.
	got := ids(nodeTeamA, "/v1/objects?tag="+tag)
	if len(got) != 2 || got[0] == e.objZ || got[1] == e.objZ {
		t.Fatalf("tenant admin tag: %v (objZ %s must not be listed)", got, e.objZ)
	}

	// Audit by workload.
	type rec struct {
		ObjectID string `json:"objectID"`
		TenantID string `json:"tenantID"`
		Kind     string `json:"kind"`
	}
	audit := func(who, path string) []rec {
		t.Helper()
		var page struct{ Items []rec }
		if st, eb := e.call(who, "GET", path, nil, &page); st != 200 {
			t.Fatalf("%s: %d %v", path, st, eb)
		}
		return page.Items
	}
	z := audit(nodeAdm, "/v1/audit?workload=z&limit=500")
	sawDeploy, sawObj := false, false
	for _, r := range z {
		if r.TenantID != e.tn2 {
			t.Fatalf("workload z returned %+v", r)
		}
		sawDeploy = sawDeploy || r.Kind == "deploy"
		sawObj = sawObj || r.ObjectID == e.objZ
	}
	if !sawDeploy || !sawObj {
		t.Fatalf("workload z: deploy %v, object %v: %+v", sawDeploy, sawObj, z)
	}
	for _, r := range audit(nodeAdm, "/v1/audit?workload=w&limit=500") {
		if r.TenantID != e.tn || r.ObjectID == e.objZ {
			t.Fatalf("workload w returned %+v", r)
		}
	}
	if r := audit(nodeTeamA, "/v1/audit?workload=z&limit=500"); len(r) != 0 {
		t.Fatalf("tenant admin saw another tenant's workload: %+v", r)
	}
	if r := audit(nodeAdm, "/v1/audit?workload=nope"); len(r) != 0 {
		t.Fatalf("unknown workload: %+v", r)
	}
}
