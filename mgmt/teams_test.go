package mgmt

import (
	"strings"
	"testing"
)

// Several tenants on one tailnet (ADR-043 D4, D5, D7).

func TestTeamsTenantRules(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c := e.c
	mk := func(extra map[string]any) map[string]any {
		body := map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	// The first tenant on a tailnet needs neither.
	a := v1Tenant(t, e, "t.ts.net", nil)
	// A second one: refused while the first still uses tag:tw.
	eb := c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "b"}), 409, "shared_owner_tag")
	if s, _ := eb["snippet"].(string); !strings.Contains(s, "tw tenant set "+a+" --owner-tag") {
		t.Fatalf("snippet = %q", s)
	}
	// Move the first to its own owner tag (D7); then the second needs its own too.
	c.ok("PATCH", "/v1/tenants/"+a, map[string]any{"ownerTag": "tag:tw-t-a"}, nil)
	c.fail("POST", "/v1/tenants", mk(map[string]any{"namePrefix": "b"}), 409, "shared_owner_tag")
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-a", "namePrefix": "b"}), 409, "shared_owner_tag")
	// Owner tag set, no prefix: both would be unprefixed.
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-b"}), 409, "name_prefix_required")
	// tailworks' own tags are never an owner tag; prefixes are short labels.
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-host", "namePrefix": "b"}), 400, "reserved_tag")
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "Bad_Prefix"}), 400, "bad_request")
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "waytoolongprefix"}), 400, "bad_request")
	b := v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "b"})
	// A third: its prefix may not clash with b's ("b-x" names would collide).
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-c", "namePrefix": "b-x"}), 409, "name_prefix_required")
	c.fail("POST", "/v1/tenants", mk(map[string]any{"ownerTag": "tag:tw-t-c", "namePrefix": "b"}), 409, "name_prefix_required")
	v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-c", "namePrefix": "c"})
	// Another tailnet is unaffected.
	v1Tenant(t, e, "other.ts.net", nil)

	// Names: b's are prefixed; a's (no prefix) are as before.
	v1Host(t, e, "h1")
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": b, "workload": "api", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, &dep)
	if dep.Endpoint.Node != "b-api-h1" {
		t.Fatalf("b's endpoint node = %q, want b-api-h1", dep.Endpoint.Node)
	}
	var o CreateObjectResponse
	e.asHost("h1").ok("POST", "/v1/objects", map[string]any{"tenantID": b, "workload": "api", "namespace": "A", "objectName": "x"}, &o)
	if o.Hostname != "b-"+o.ObjectID {
		t.Fatalf("b's object hostname = %q, want b-%s (the ID itself unchanged)", o.Hostname, o.ObjectID)
	}
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a, "workload": "api", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, &dep)
	if dep.Endpoint.Node != "api-h1" {
		t.Fatalf("a's endpoint node = %q, want api-h1", dep.Endpoint.Node)
	}
	// a (unprefixed) can't deploy a workload under b's prefix.
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": a, "workload": "b-api", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, 400, "bad_request")
	// No workload may ask for an owner tag.
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": a, "workload": "w", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:tw-t-b"}}}}, 400, "reserved_tag")
	// The prefix is fixed once b has deployed.
	c.fail("PATCH", "/v1/tenants/"+b, map[string]any{"namePrefix": "bb"}, 400, "immutable_field")
}

func TestTeamsTagOwnership(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = `{"tagOwners": {
		"tag:a-api": ["autogroup:admin", "tag:tw-t-a"],
		"tag:b-api": ["autogroup:admin", "tag:tw-t-b"],
		"tag:both":  ["tag:tw-t-a", "tag:tw-t-b"],
		"tag:ops":   ["autogroup:admin", "tag:tw"]
	}}`
	a := v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-a"})
	v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "b"})
	v1Host(t, e, "h1")
	deploy := func(tag string) map[string]any {
		return map[string]any{"tenantID": a, "workload": "api", "host": "h1", "bundle": noConfigBundle(t),
			"namespaces": []ClassConfig{{Class: "A", Tags: []string{tag}}}}
	}
	e.c.ok("POST", "/v1/deployments", deploy("tag:a-api"), nil)
	// Another team's tag, a tag both teams' owner tags own, and one only
	// tag:tw owns: refused, with the tagOwners line to use.
	for _, tag := range []string{"tag:b-api", "tag:both", "tag:ops"} {
		eb := e.c.fail("POST", "/v1/deployments", deploy(tag), 400, "tag_not_owned")
		if s, _ := eb["snippet"].(string); !strings.Contains(s, `"tag:tw-t-a"`) {
			t.Fatalf("%s: snippet %q", tag, s)
		}
	}
	// The preflight reports it and the snippet names the owner tag.
	var pf PreflightResult
	e.c.ok("POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": a, "workload": "api", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:new"}}}}, &pf)
	if pf.OK || !strings.Contains(pf.TagOwnersSnippet, `"tag:new": ["autogroup:admin", "tag:tw-t-a"]`) {
		t.Fatalf("preflight = %+v", pf)
	}
	e.c.ok("POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": a, "workload": "api", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:b-api"}}}}, &pf)
	if pf.OK || len(pf.TagOwnerProblems) != 1 || !strings.Contains(pf.TagOwnerProblems[0], "tag:tw-t-b") {
		t.Fatalf("preflight = %+v", pf)
	}
}

func TestTeamsPublicSnippet(t *testing.T) {
	t.Parallel()
	const want = `"tagOwners": { "tag:tw-public": ["autogroup:admin", "tag:tw"] },
"nodeAttrs": [ { "target": ["tag:tw-public"], "attr": ["funnel"] } ]`
	if s := publicPart(Tenant{}).Inline(); s != want {
		t.Fatalf("default: %s", s)
	}
	if s := publicPart(Tenant{OwnerTag: "tag:tw-t-a"}).Inline(); !strings.Contains(s, `"tag:tw-public": ["autogroup:admin", "tag:tw", "tag:tw-t-a"]`) {
		t.Fatalf("owner tag: %s", s)
	}
	if !prefixesClash("a", "a-b") || prefixesClash("a", "ab") || !prefixesClash("x", "x") {
		t.Fatal("prefixesClash")
	}
}

func TestTeamsServiceName(t *testing.T) {
	t.Parallel()
	cfg := &TwConfig{}
	cfg.Tailnet.Service = []byte("true")
	classes := []ClassConfig{{Class: WorkloadNamespace, Tags: []string{"tag:x"}}}
	name, err := applyTeamService(Tenant{NamePrefix: "b"}, cfg, "api", classes)
	if err != nil || name != "svc:b-api" || classes[0].Service != "svc:b-api" {
		t.Fatalf("prefixed: %q %v %+v", name, err, classes)
	}
	cfg.Tailnet.Service = []byte(`"svc:web"`)
	if name, _ := applyTeamService(Tenant{}, cfg, "api", classes); name != "svc:web" {
		t.Fatalf("no prefix: %q", name)
	}
	if name, _ := applyTeamService(Tenant{NamePrefix: "b"}, cfg, "api", classes); name != "svc:b-web" {
		t.Fatalf("named, prefixed: %q", name)
	}
}
