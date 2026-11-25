package mgmt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// Store side of deployment history (ADR-046): newest-first
// paging, the recorded hash, active_at, and pruning that keeps the hash and
// the newest bundles. SQLite always; Postgres with TW_TEST_POSTGRES_DSN
// (make test-pg).
func TestStoreDeploymentHistory(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		bundles := [][]byte{[]byte("bundle-0"), []byte("bundle-1"), nil, []byte("bundle-3"), []byte("bundle-4")}
		ids := []string{"dp_h0", "dp_h1", "dp_h2", "dp_h3", "dp_h4"}
		for i, b := range bundles {
			d := Deployment{ID: ids[i], TenantID: "tn", Workload: "web", HostID: "h1", Namespaces: []ClassConfig{{Class: WorkloadNamespace, Tags: []string{"tag:web"}}},
				Epoch: int64(i + 1), Bundle: b, CreatedAt: int64(1000 + i)}
			if err := s.CreateDeployment(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		// Another workload's deployments are never listed or pruned.
		if err := s.CreateDeployment(ctx, Deployment{ID: "dp_other", TenantID: "tn", Workload: "api", HostID: "h1", Bundle: []byte("x"), CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		// Only dp_h4's hash is recorded at deploy; pruning must hash the rest.
		if err := s.SetDeploymentMeta(ctx, "dp_h4", bundleSHA256(bundles[4]), "dp_h1"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDeploymentMeta(ctx, "dp_nope", "", ""); err != ErrNotFound {
			t.Fatalf("meta of an unknown deployment: %v", err)
		}

		all, err := s.DeploymentHistory(ctx, "tn", "web", Page{})
		if err != nil || len(all) != 5 {
			t.Fatalf("history: %d %v", len(all), err)
		}
		for i, d := range all {
			if d.ID != ids[4-i] {
				t.Fatalf("order: %d is %s, want %s", i, d.ID, ids[4-i])
			}
		}
		if all[0].BundleSHA256 != bundleSHA256(bundles[4]) || all[0].RollbackOf != "dp_h1" || !all[0].BundleRetained || all[0].BundleBytes != int64(len(bundles[4])) {
			t.Fatalf("newest: %+v", all[0])
		}
		if all[2].BundleRetained || all[2].BundlePruned || all[2].BundleBytes != 0 {
			t.Fatalf("bundle-less deployment: %+v", all[2])
		}
		// Paging: two at a time, newest first, nothing repeated.
		var paged []string
		pg := Page{Limit: 2}
		for {
			recs, err := s.DeploymentHistory(ctx, "tn", "web", pg)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range recs {
				paged = append(paged, d.ID)
			}
			if len(recs) < 2 {
				break
			}
			last := recs[len(recs)-1]
			pg.After = &PageKey{Created: last.CreatedAt, ID: last.ID}
		}
		if len(paged) != 5 || paged[0] != "dp_h4" || paged[4] != "dp_h0" {
			t.Fatalf("paged: %v", paged)
		}

		// active_at: only the assigned host, only once.
		if err := s.MarkDeploymentActive(ctx, "dp_h3", "h2", 5000); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDeploymentActive(ctx, "dp_h3", "h1", 6000); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDeploymentActive(ctx, "dp_h3", "h1", 7000); err != nil {
			t.Fatal(err)
		}
		if d, _ := s.DeploymentRecordByID(ctx, "dp_h3"); d.ActiveAt != 6000 {
			t.Fatalf("active_at %d, want 6000", d.ActiveAt)
		}

		// keep <= 0 prunes nothing.
		if p, err := s.PruneBundles(ctx, "tn", "web", 0, 9000); err != nil || len(p) != 0 {
			t.Fatalf("keep 0: %v %v", p, err)
		}
		// Keep 2: dp_h4 and dp_h3 keep their bundles (dp_h2 has none);
		// dp_h1 and dp_h0 lose theirs but keep the hash.
		p, err := s.PruneBundles(ctx, "tn", "web", 2, 9000)
		if err != nil || len(p) != 2 || p[0] != "dp_h1" || p[1] != "dp_h0" {
			t.Fatalf("pruned %v %v", p, err)
		}
		if p, _ := s.PruneBundles(ctx, "tn", "web", 2, 9001); len(p) != 0 {
			t.Fatalf("second prune: %v", p)
		}
		for i, id := range []string{"dp_h0", "dp_h1"} {
			d, err := s.DeploymentRecordByID(ctx, id)
			if err != nil || d.BundleRetained || !d.BundlePruned || d.BundleSHA256 != bundleSHA256(bundles[i]) || d.BundleBytes != 0 {
				t.Fatalf("%s after prune: %+v %v", id, d, err)
			}
			full, err := s.GetDeployment(ctx, id)
			if err != nil || len(full.Bundle) != 0 {
				t.Fatalf("%s still has %d bytes (%v)", id, len(full.Bundle), err)
			}
		}
		for _, id := range []string{"dp_h3", "dp_h4"} {
			if full, _ := s.GetDeployment(ctx, id); len(full.Bundle) == 0 {
				t.Fatalf("%s lost its bundle", id)
			}
		}
		if full, _ := s.GetDeployment(ctx, "dp_other"); len(full.Bundle) == 0 {
			t.Fatal("another workload's bundle was pruned")
		}
	})
}

// The API: history, rollback to the previous good deployment, an explicit
// target, the refusals (no target, the current one, a pruned bundle, an
// unknown ID), the audit kind and retention. Every exchange is checked
// against api/openapi.yaml.
func TestV1DeployHistoryRollback(t *testing.T) {
	t.Parallel()
	e := newTestEnvWith(t, func(c *Config) { c.BundleRetention = 2 })
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	h1 := v1Host(t, e, "h1")
	c := e.c
	ctx := context.Background()
	built := map[string][]byte{} // makeBundle's tar order varies: build each version once
	bundleOf := func(v string) []byte {
		if built[v] == nil {
			built[v] = makeBundle(t, map[string]string{"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"] } }`, "dist/index.js": "export default {} // " + v})
		}
		return built[v]
	}
	hist := "/v1/tenants/" + tn + "/workloads/web/deployments"
	rb := "/v1/tenants/" + tn + "/workloads/web/rollbacks"
	deploy := func(v string) DeploymentView {
		t.Helper()
		time.Sleep(2 * time.Millisecond) // distinct createdAt
		var d DeploymentView
		c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "web", "bundle": base64.StdEncoding.EncodeToString(bundleOf(v))}, &d)
		return d
	}
	report := func(d DeploymentView, status, errText string) {
		t.Helper()
		st := map[string]any{"deploymentID": d.DeploymentID, "status": status}
		if errText != "" {
			st["error"] = errText
		}
		h1.ok("PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 1, "leases": []map[string]any{{"namespace": LeaseName(tn, "web"), "epoch": d.Epoch}},
			"deployments": []map[string]any{st}}, nil)
	}
	history := func() historyPage {
		t.Helper()
		var p historyPage
		c.ok("GET", hist, nil, &p)
		return p
	}
	type rollback struct {
		RollbackOf string
		Target     DeploymentRecord
		Deployment DeploymentView
	}

	c.fail("GET", hist, nil, 404, "unknown_workload")
	c.fail("POST", rb, map[string]any{}, 404, "unknown_workload")

	d1 := deploy("v1")
	report(d1, "active", "")
	p := history()
	if len(p.Items) != 1 || p.BundleRetention != 2 {
		t.Fatalf("history after one deploy: %+v", p)
	}
	if r := p.Items[0]; r.ID != d1.DeploymentID || r.Status != "active" || r.ActiveAt == 0 || !r.BundleRetained || r.BundleSHA256 != bundleSHA256(bundleOf("v1")) {
		t.Fatalf("record: %+v", r)
	}
	c.fail("POST", rb, map[string]any{}, 409, "no_rollback_target")

	d2 := deploy("v2")
	report(d2, "failed", "boom")
	c.fail("POST", rb, map[string]any{"deploymentID": d2.DeploymentID}, 409, "no_rollback_target") // the current one
	c.fail("POST", rb, map[string]any{"deploymentID": "dp_nope"}, 404, "not_found")

	// dryRun preflights the target's bundle and deploys nothing.
	var pf PreflightResult
	if st := c.okStatus("POST", rb+"?dryRun=true", map[string]any{}, &pf); st != 200 || len(pf.SrcTags) == 0 {
		t.Fatalf("dryRun: %d %+v", st, pf)
	}
	if p := history(); len(p.Items) != 2 {
		t.Fatalf("dryRun deployed: %d deployments", len(p.Items))
	}

	// Rollback with no ID: the previous good one (d1).
	var r1 rollback
	if st := c.okStatus("POST", rb, map[string]any{}, &r1); st != 202 {
		t.Fatalf("rollback: %d", st)
	}
	d3 := r1.Deployment
	if r1.RollbackOf != d1.DeploymentID || r1.Target.ID != d1.DeploymentID || d3.DeploymentID == d1.DeploymentID || d3.Host != "h1" || d3.Epoch <= d2.Epoch {
		t.Fatalf("rollback: %+v (d2 epoch %d)", r1, d2.Epoch)
	}
	if full, _ := e.store.GetDeployment(ctx, d3.DeploymentID); !bytes.Equal(full.Bundle, bundleOf("v1")) {
		t.Fatal("the rollback did not redeploy d1's bundle")
	}
	recs, _, err := e.m.QueryAudit(ctx, AuditFilter{TenantID: tn, Kinds: []string{"rollback"}})
	if err != nil || len(recs) != 1 {
		t.Fatalf("rollback audit: %d %v", len(recs), err)
	}
	var detail map[string]any
	json.Unmarshal(recs[0].Detail, &detail)
	if detail["rollbackOf"] != d1.DeploymentID || detail["deploymentID"] != d3.DeploymentID {
		t.Fatalf("audit detail %v", detail)
	}
	report(d3, "active", "")

	// Retention 2: d3 and d2 keep their bundles; d1's is pruned.
	p = history()
	if len(p.Items) != 3 || p.Items[0].ID != d3.DeploymentID || p.Items[0].RollbackOf != d1.DeploymentID || p.Items[0].Status != "active" {
		t.Fatalf("history after rollback: %+v", p.Items)
	}
	if r := p.Items[1]; r.ID != d2.DeploymentID || r.Status != "superseded" || r.HostStatus != "failed" || r.Error != "boom" || !r.BundleRetained {
		t.Fatalf("d2: %+v", r)
	}
	if r := p.Items[2]; r.ID != d1.DeploymentID || r.BundleRetained || r.BundleSHA256 != bundleSHA256(bundleOf("v1")) {
		t.Fatalf("d1: %+v", r)
	}
	// Paging the history.
	var first []DeploymentRecord
	if next := c.items(hist+"?limit=2", &first); next == nil || len(first) != 2 {
		t.Fatalf("page 1: %d %v", len(first), next)
	} else {
		var rest []DeploymentRecord
		c.items(hist+"?limit=2&cursor="+*next, &rest)
		if len(rest) != 1 || rest[0].ID != d1.DeploymentID {
			t.Fatalf("page 2: %+v", rest)
		}
	}

	// d1's bundle is gone: naming it fails cleanly, and nothing is deployed.
	c.fail("POST", rb, map[string]any{"deploymentID": d1.DeploymentID}, 409, "bundle_pruned")
	c.fail("POST", rb+"?dryRun=true", map[string]any{"deploymentID": d1.DeploymentID}, 409, "bundle_pruned")
	// With no ID: d2 failed and d1 has the running bundle: nothing to go back to.
	c.fail("POST", rb, map[string]any{}, 409, "no_rollback_target")
	if p := history(); len(p.Items) != 3 {
		t.Fatalf("a refused rollback deployed: %d deployments", len(p.Items))
	}

	// A new version, then rollback with no ID goes back to d3 (it ran,
	// and its bundle differs).
	d4 := deploy("v4")
	report(d4, "active", "")
	var r2 rollback
	c.ok("POST", rb, map[string]any{}, &r2)
	if r2.RollbackOf != d3.DeploymentID {
		t.Fatalf("second rollback went to %s, want %s", r2.RollbackOf, d3.DeploymentID)
	}
	// An explicit target is used as named (d4: superseded, still retained).
	c.ok("POST", rb, map[string]any{"deploymentID": d4.DeploymentID}, &r2)
	if r2.RollbackOf != d4.DeploymentID {
		t.Fatalf("explicit rollback: %+v", r2)
	}
}

// Rollback and history have deploy's role: a host may not call them.
func TestAuthzRollbackIsAdminOnly(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	a.fail(nodeH1, "GET", "/v1/tenants/"+a.tn+"/workloads/w/deployments", nil, 403, "forbidden")
	a.fail(nodeH1, "POST", "/v1/tenants/"+a.tn+"/workloads/w/rollbacks", map[string]any{}, 403, "forbidden")
	if st, eb := a.call(nodeAdm, "GET", "/v1/tenants/"+a.tn+"/workloads/w/deployments", nil, nil); st != 200 {
		t.Fatalf("admin history: %d %v", st, eb)
	}
	a.fail(nodeAdm, "POST", "/v1/tenants/"+a.tn+"/workloads/w/rollbacks", map[string]any{}, 409, "no_rollback_target")
}
