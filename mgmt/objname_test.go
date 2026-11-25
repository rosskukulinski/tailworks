package mgmt

import (
	"context"
	"testing"
)

// TestCreateObjectRefusesControlNames (#185, ledger H13/F9 input half):
// CreateObject refuses an object name with a C0, DEL, C1 or bidi control
// with 400 bad_object_name and creates nothing. Names already in the store
// are not backfilled (they render through internal/termsafe).
func TestCreateObjectRefusesControlNames(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, nil)
	for _, name := range []string{"a\x1b[31mb", "a\x00", "a\r\nb", "a\x7f", "a\u009bb", "a\u202eb"} {
		h1.fail("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "namespace": "SupportAgent", "objectName": name}, 400, "bad_object_name")
	}
	var objs []Object
	c.items("/v1/objects?tenant="+tn.TenantID, &objs)
	for _, o := range objs {
		if o.Namespace == "SupportAgent" {
			t.Errorf("an object was created: %+v", o)
		}
	}
	var ok CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "namespace": "SupportAgent", "objectName": "café 1"}, &ok)

	// No backfill: an object stored with such a name before this check is
	// still answered idempotently.
	old := "old\x1bname"
	id := e.m.objectID(tn.TenantID, "support", "SupportAgent", old)
	if err := e.store.CreateObject(context.Background(), Object{ID: id, TenantID: tn.TenantID, Workload: "support", Namespace: "SupportAgent", Name: old, Tags: []string{"tag:support"},
		Hostname: id, Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	var again CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "namespace": "SupportAgent", "objectName": old}, &again)
	if again.ObjectID != id {
		t.Errorf("existing object answered %+v, want %s", again, id)
	}
}
