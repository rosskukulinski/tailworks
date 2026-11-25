package mgmt

import "testing"

// GET /v1/objects/{id}/certificate is deprecated: it still serves the same
// certificate as GET /v1/certificates/{stableNodeID}, and says so in the
// Deprecation and Link headers (ADR-041 section 9).
func TestObjectCertificateDeprecated(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, nil)
	var root CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.TenantID, "namespace": "SupportAgent", "objectName": "alice"}, &root)
	h1.ok("POST", "/v1/objects/"+root.ObjectID+"/credentials?epoch=1", nil, &Credential{})

	// Before ready: 409 not_ready, still marked deprecated.
	st, eb, hdr := c.doRaw("GET", "/v1/objects/"+root.ObjectID+"/certificate", nil, nil)
	if st != 409 || eb["code"] != "not_ready" || hdr.Get("Deprecation") != objectCertDeprecatedAt {
		t.Fatalf("before ready: %d %v Deprecation=%q", st, eb, hdr.Get("Deprecation"))
	}

	h1.ok("PATCH", "/v1/objects/"+root.ObjectID, map[string]any{"status": "ready", "epoch": 1, "nodeID": "1", "stableNodeID": "nROOT", "magicDNS": root.ObjectID + ".t.ts.net"}, nil)
	var old, cur NodeCertView
	st, _, hdr = c.doRaw("GET", "/v1/objects/"+root.ObjectID+"/certificate", nil, &old)
	if st != 200 || hdr.Get("Deprecation") != objectCertDeprecatedAt {
		t.Fatalf("deprecated route: %d Deprecation=%q", st, hdr.Get("Deprecation"))
	}
	succ := "/v1/certificates/nROOT?tenant=" + tn.TenantID
	if want := "<" + succ + `>; rel="successor-version"`; hdr.Get("Link") != want {
		t.Fatalf("Link = %q, want %q", hdr.Get("Link"), want)
	}
	st, _, hdr = c.doRaw("GET", succ, nil, &cur)
	if st != 200 || hdr.Get("Deprecation") != "" {
		t.Fatalf("successor: %d Deprecation=%q", st, hdr.Get("Deprecation"))
	}
	if old.ObjectID != root.ObjectID || old.Cert == "" || old.Cert != cur.Cert {
		t.Fatalf("certificates differ: %+v vs %+v", old, cur)
	}
}
