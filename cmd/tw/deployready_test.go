package main

import (
	"net/http"
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestPromoteRollbackReportCrashloop: a promoted or rolled-back deployment
// whose workerd keeps exiting is reported as deploy_crashloop, with the
// host's detail lines and the `tw logs` hint, the way deploy reports it.
// Before readyWait (deployready.go) only deploy knew the state; promote and
// rollback waited out --wait and said identity_pending (#525).
func TestPromoteRollbackReportCrashloop(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		run  func([]string) error
		post string
		args []string
	}{
		{"promote", cmdPromote, "/v1/tenants/tn_1/workloads/app/promotions", []string{"app", "preview"}},
		{"rollback", cmdRollback, "/v1/tenants/tn_1/workloads/app/rollbacks", []string{"app"}},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			dir := fakeLinkedMgmt(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == tc.post && r.URL.Query().Get("dryRun") == "true":
					writeJSON(w, mgmt.PreflightResult{OK: true})
				case r.Method == http.MethodPost && r.URL.Path == tc.post:
					writeJSON(w, map[string]any{"deployment": mgmt.DeploymentView{DeploymentID: "dep_1"}})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/deployments/dep_1":
					writeJSON(w, mgmt.DeploymentView{DeploymentID: "dep_1", Status: "crashloop", Error: "attempt 4\nworkerd exited with status 1"})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			// --wait=0s: the crashloop must win over the spent wait budget.
			err := tc.run(append([]string{"--state-dir", dir, "--json", "--wait=0s"}, tc.args...))
			if got := cliCode(err); got != "deploy_crashloop" {
				t.Fatalf("tw %s: code %q (%v), want deploy_crashloop", tc.cmd, got, err)
			}
			ce := asCLIErr(err)
			if !slices.Equal(ce.Lines, []string{"attempt 4", "workerd exited with status 1"}) {
				t.Errorf("tw %s: detail lines %q, want the host's two lines", tc.cmd, ce.Lines)
			}
			if !slices.Equal(ce.Fix, []string{"tw logs app"}) {
				t.Errorf("tw %s: fix %q, want [tw logs app]", tc.cmd, ce.Fix)
			}
		})
	}
}
