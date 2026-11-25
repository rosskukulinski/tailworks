package mgmt

import (
	"net/http"
	"sort"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// GetHostSandboxDeny is GET /v1/hosts/{id}/sandbox-deny: every tenant's
// credential file paths, and only those, for the host's sandbox deny list.
// GET /v1/tenants shows a host only the tenants it runs (M3), which left a
// workload started before another tenant's first deploy able to read that
// tenant's API key file (TestCLIMultiTenant check (d)). Paths only, so a
// host still learns no other tenant's ID or tailnet.
func (a *v1api) GetHostSandboxDeny(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	if _, err := a.s.M.store.GetHost(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	ts, err := a.s.M.store.ListTenants(r.Context(), Page{})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, t := range ts {
		for _, p := range []string{t.APIKeyFile, t.OAuthClientFile} {
			if p != "" && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	writeJSON(w, 200, map[string]any{"paths": paths})
}
