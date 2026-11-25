package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw expose names | release (issue #521): the CLI callers of
// GET/DELETE /v1/tenants/{id}/public-names.
func TestCLIExposeNames(t *testing.T) {
	e := newPubEnv(t)
	if out, err := e.call(t, cmdExpose, "names"); err != nil || !strings.Contains(out, "owns no public names") {
		t.Fatalf("names before any deploy: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdExpose, "names", "extra"); cliCode(err) != "bad_request" {
		t.Fatalf("names extra: %v", err)
	}
	if _, err := e.call(t, cmdExpose, "release"); cliCode(err) != "bad_request" {
		t.Fatalf("release without a name: %v", err)
	}
	e.setPolicy(`{}`)
	e.call(t, cmdTenantSet, e.tenantID, "--public-ingress", "allow")
	if out, err := e.call(t, cmdDeploy, writePubApp(t), "--allow-missing-grants", "--no-wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	out, err := e.call(t, cmdExpose, "names", "--json")
	var r exposeNamesResult
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || len(r.Names) != 1 || r.Names[0].Name != "support" || r.TenantID != e.tenantID {
		t.Fatalf("names --json: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdExpose, "names"); err != nil || !strings.Contains(out, "support") || !strings.Contains(out, "tw expose release <name>") {
		t.Fatalf("names: %v\n%s", err, out)
	}
	// Served: refused, with mgmt's fix.
	if _, err := e.call(t, cmdExpose, "release", "support"); cliCode(err) != "public_name_in_use" || !strings.Contains(cliText(err), "tw undeploy support") {
		t.Fatalf("release while served: %v\n%s", err, cliText(err))
	}
	if _, err := e.call(t, cmdExpose, "release", "nope"); cliCode(err) != "not_found" || !strings.Contains(cliText(err), "tw expose names") {
		t.Fatalf("release of a name not owned: %v\n%s", err, cliText(err))
	}
	if out, err := e.call(t, cmdUndeploy, "support"); err != nil {
		t.Fatalf("undeploy: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdExpose, "release", "support"); err != nil || !strings.Contains(out, "released   public name support") || !strings.Contains(out, "any tenant on the tailnet") {
		t.Fatalf("release: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdExpose, "names"); err != nil || !strings.Contains(out, "owns no public names") {
		t.Fatalf("names after release: %v\n%s", err, out)
	}
	recs, _ := e.st.ListAudit(context.Background(), mgmt.AuditFilter{TenantID: e.tenantID, Kinds: []string{"public_name_release"}})
	if len(recs) != 1 || recs[0].ActingFor == "" {
		t.Fatalf("release audit (with the actor): %+v", recs)
	}
}
