package mgmt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// Preview deploys and promotion (ADR-048): a preview is a
// workload of its own next to the workload, which it never touches; it
// takes no Service or replicas, gets the workload's secrets, and promote
// deploys its bundle to the workload through Deploy, then undeploys it.
func TestV1PreviewPromote(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	h1 := v1Host(t, e, "h1")
	c := e.c
	ctx := context.Background()
	built := map[string][]byte{}
	bundleOf := func(v string) []byte {
		if built[v] == nil {
			built[v] = makeBundle(t, map[string]string{"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"], "service": true } }`, "dist/index.js": "export default {} // " + v})
		}
		return built[v]
	}
	b64 := func(v string) string { return base64.StdEncoding.EncodeToString(bundleOf(v)) }
	deploy := func(body map[string]any) DeploymentView {
		t.Helper()
		time.Sleep(2 * time.Millisecond) // distinct createdAt
		body["tenantID"] = tn
		if body["workload"] == nil {
			body["workload"] = "web"
		}
		var d DeploymentView
		c.ok("POST", "/v1/deployments", body, &d)
		return d
	}
	report := func(d DeploymentView, status string) {
		t.Helper()
		h1.ok("PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 1, "leases": []map[string]any{{"namespace": LeaseName(tn, d.Workload), "epoch": d.Epoch}},
			"deployments": []map[string]any{{"deploymentID": d.DeploymentID, "status": status}}}, nil)
	}
	previews := func() []Preview {
		t.Helper()
		var p struct{ Items []Preview }
		c.ok("GET", "/v1/tenants/"+tn+"/workloads/web/previews", nil, &p)
		return p.Items
	}
	promote := "/v1/tenants/" + tn + "/workloads/web/promotions"

	if ps := previews(); len(ps) != 0 {
		t.Fatalf("previews before any: %+v", ps)
	}
	prod := deploy(map[string]any{"bundle": b64("v1")})
	report(prod, "active")
	c.ok("PUT", "/v1/tenants/"+tn+"/workloads/web/secrets/API_KEY", map[string]string{"value": secretValue}, nil)
	prodLease, _ := e.store.GetLease(ctx, LeaseName(tn, "web"))

	// A preview with the default alias: its own workload, lease and node;
	// the workload's deployment and lease are untouched.
	p1 := deploy(map[string]any{"bundle": b64("v2"), "preview": true})
	alias := bundleSHA256(bundleOf("v2"))[:8]
	if p1.Workload != "web--preview-"+alias || previewOf(p1.Namespaces) != "web" {
		t.Fatalf("preview: workload %s namespaces %+v", p1.Workload, p1.Namespaces)
	}
	if p1.Endpoint.Service != nil || workloadService(p1.Namespaces) != "" {
		t.Fatalf("a preview joined the workload's Service: %+v", p1.Endpoint.Service)
	}
	if l, _ := e.store.GetLease(ctx, LeaseName(tn, "web")); l.Epoch != prodLease.Epoch || l.Holder != prodLease.Holder {
		t.Fatalf("the preview moved the workload's lease: %+v -> %+v", prodLease, l)
	}
	if l, err := e.store.GetLease(ctx, LeaseName(tn, p1.Workload)); err != nil || l.Epoch != p1.Epoch {
		t.Fatalf("the preview's own lease: %+v %v", l, err)
	}
	if v, _ := e.m.GetDeployment(ctx, prod.DeploymentID); v.Status != "active" {
		t.Fatalf("the workload's deployment after a preview: %s", v.Status)
	}
	// The same bundle is the same preview (a redeploy at epoch+1).
	if again := deploy(map[string]any{"bundle": b64("v2"), "preview": true}); again.Workload != p1.Workload || again.Epoch <= p1.Epoch {
		t.Fatalf("same bundle: %s epoch %d (was %s %d)", again.Workload, again.Epoch, p1.Workload, p1.Epoch)
	} else {
		p1 = again
	}
	report(p1, "active")

	// Secrets are the workload's; stores are the preview's own.
	if got, err := e.m.ReleaseSecrets(ctx, tn, p1.Workload, "h1", p1.Epoch); err != nil || got["API_KEY"] != secretValue {
		t.Fatalf("preview secrets: %v %v", got, err)
	}
	if owner, err := e.m.storeScope(ctx, tn, p1.Workload, "kv", "sessions"); err != nil || owner != p1.Workload {
		t.Fatalf("preview store scope: %s %v", owner, err)
	}

	// A named alias.
	p2 := deploy(map[string]any{"bundle": b64("v3"), "previewAlias": "pr-42"})
	if p2.Workload != "web--preview-pr-42" {
		t.Fatalf("aliased preview: %s", p2.Workload)
	}
	// Redeploying a preview by its own name (host drain) keeps it a preview.
	if r, err := e.m.Deploy(ctx, DeployRequest{TenantID: tn, Workload: p2.Workload, Bundle: bundleOf("v3"), By: "test"}); err != nil {
		t.Fatal(err)
	} else if v, _ := e.m.GetDeployment(ctx, r.DeploymentID); previewOf(v.Namespaces) != "web" || workloadService(v.Namespaces) != "" {
		t.Fatalf("redeploy by name: %+v", v.Namespaces)
	}
	if ps := previews(); len(ps) != 2 || ps[0].Workload != p2.Workload || ps[0].Alias != "pr-42" || ps[1].Alias != alias || ps[1].BundleSHA256 != bundleSHA256(bundleOf("v2")) {
		t.Fatalf("previews: %+v", ps)
	}

	// Refusals.
	if _, err := e.m.Deploy(ctx, DeployRequest{TenantID: tn, Workload: "web", Bundle: bundleOf("v2"), PreviewAlias: "Bad_Alias"}); previewErrCode(err) != "bad_request" {
		t.Fatalf("bad alias: %v", err)
	}
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": p1.Workload, "bundle": b64("v2"), "preview": true}, 400, "preview_unsupported")
	queued := makeBundle(t, map[string]string{"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"] } }`,
		"wrangler.jsonc": `{"queues":{"consumers":[{"queue":"jobs"}]}}`, "dist/index.js": "export default {}"})
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "web", "bundle": base64.StdEncoding.EncodeToString(queued), "preview": true}, 400, "preview_unsupported")
	deploy(map[string]any{"workload": "web--preview-taken", "bundle": b64("v1")}) // a workload of that name, not a preview
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "web", "bundle": b64("v1"), "previewAlias": "taken"}, 409, "preview_conflict")

	if _, _, _, err := e.m.Promote(ctx, PromoteRequest{TenantID: tn, Workload: "web"}); previewErrCode(err) != "bad_request" {
		t.Fatalf("promote without a preview: %v", err)
	}
	c.fail("POST", promote, map[string]any{"preview": "nope"}, 404, "unknown_preview")
	c.fail("POST", promote, map[string]any{"preview": "taken"}, 404, "unknown_preview")
	failed := deploy(map[string]any{"bundle": b64("v4"), "previewAlias": "bad"})
	report(failed, "failed")
	c.fail("POST", promote, map[string]any{"preview": "bad"}, 409, "preview_failed")

	// dryRun preflights the preview's bundle and deploys nothing.
	var pf PreflightResult
	if st := c.okStatus("POST", promote+"?dryRun=true", map[string]any{"preview": alias}, &pf); st != 200 || len(pf.SrcTags) == 0 {
		t.Fatalf("dryRun: %d %+v", st, pf)
	}
	if l, _ := e.store.GetLease(ctx, LeaseName(tn, "web")); l.Epoch != prodLease.Epoch {
		t.Fatal("dryRun deployed")
	}

	// Promote by alias, keeping the preview.
	type promotion struct {
		Preview        string
		PromotedFrom   string
		Deployment     DeploymentView
		PreviewStopped bool
	}
	var pr promotion
	if st := c.okStatus("POST", promote, map[string]any{"preview": p2.Workload, "keepPreview": true}, &pr); st != 202 {
		t.Fatalf("promote: %d", st)
	}
	if pr.PreviewStopped || pr.Preview != p2.Workload || pr.Deployment.Workload != "web" {
		t.Fatalf("promote keepPreview: %+v", pr)
	}
	// Promote by alias: the workload runs the preview's bundle at epoch+1,
	// on its host, with its Service; the preview is undeployed.
	if st := c.okStatus("POST", promote, map[string]any{"preview": alias}, &pr); st != 202 {
		t.Fatalf("promote: %d", st)
	}
	d := pr.Deployment
	if pr.Preview != p1.Workload || pr.PromotedFrom != p1.DeploymentID || !pr.PreviewStopped || d.Workload != "web" || d.Host != prodLease.Holder || d.Epoch <= prodLease.Epoch {
		t.Fatalf("promotion: %+v", pr)
	}
	if previewOf(d.Namespaces) != "" || workloadService(d.Namespaces) != "svc:web" {
		t.Fatalf("promoted namespaces: %+v", d.Namespaces)
	}
	if full, _ := e.store.GetDeployment(ctx, d.DeploymentID); !bytes.Equal(full.Bundle, bundleOf("v2")) {
		t.Fatal("promote did not deploy the preview's bundle")
	}
	if v, _ := e.m.GetDeployment(ctx, p1.DeploymentID); v.Status != "stopped" {
		t.Fatalf("the promoted preview: %s", v.Status)
	}
	recs, _, err := e.m.QueryAudit(ctx, AuditFilter{TenantID: tn, Kinds: []string{"promote"}})
	if err != nil || len(recs) != 2 {
		t.Fatalf("promote audit: %d %v", len(recs), err)
	}
	var detail map[string]any
	for _, rec := range recs {
		var dm map[string]any
		if json.Unmarshal(rec.Detail, &dm) == nil && dm["deploymentID"] == d.DeploymentID {
			detail = dm
		}
	}
	if detail["promotedFrom"] != p1.DeploymentID || detail["preview"] != p1.Workload || detail["deploymentID"] != d.DeploymentID {
		t.Fatalf("audit detail %v", detail)
	}
	c.fail("POST", promote, map[string]any{"preview": alias}, 404, "unknown_preview") // stopped
	for _, p := range previews() {
		if p.Workload == p1.Workload {
			t.Fatalf("a promoted preview is still listed: %+v", p)
		}
	}
}

// strip drops the Service, public routes and replicas from a preview's
// config (and nothing from anyone else's).
func TestPreviewStrip(t *testing.T) {
	t.Parallel()
	cfg := func() *TwConfig {
		var c TwConfig
		if err := json.Unmarshal([]byte(`{"replicas": 3, "public": {"routes": ["/"]}, "tailnet": {"tags": ["tag:a"], "service": true,
			"objects": {"Room": {"tags": ["tag:b"], "public": {"create": true}}}}}`), &c); err != nil {
			t.Fatal(err)
		}
		return &c
	}
	c := cfg()
	(&previewCtx{of: "web"}).strip(c)
	if c.Public != nil || c.Tailnet.Service != nil || c.Replicas != 0 || c.Tailnet.Objects["Room"].Public != nil || len(c.Tailnet.Objects["Room"].Tags) != 1 {
		t.Fatalf("stripped: %+v", c)
	}
	if pub, err := c.PublicConfig(); err != nil || pub != nil {
		t.Fatalf("public after strip: %v %v", pub, err)
	}
	for _, pc := range []*previewCtx{nil, {promotedFrom: "dp_x"}} {
		c := cfg()
		pc.strip(c)
		if c.Public == nil || c.Replicas != 3 || c.Tailnet.Objects["Room"].Public == nil {
			t.Fatalf("%+v stripped a non-preview config", pc)
		}
	}
}

func TestAuthzPreviewIsAdminOnly(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	a.fail(nodeH1, "GET", "/v1/tenants/"+a.tn+"/workloads/w/previews", nil, 403, "forbidden")
	a.fail(nodeH1, "POST", "/v1/tenants/"+a.tn+"/workloads/w/promotions", map[string]any{"preview": "x"}, 403, "forbidden")
	if st, eb := a.call(nodeAdm, "GET", "/v1/tenants/"+a.tn+"/workloads/w/previews", nil, nil); st != 200 {
		t.Fatalf("admin previews: %d %v", st, eb)
	}
	a.fail(nodeAdm, "POST", "/v1/tenants/"+a.tn+"/workloads/w/promotions", map[string]any{"preview": "x"}, 404, "unknown_preview")
}

func previewErrCode(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}
