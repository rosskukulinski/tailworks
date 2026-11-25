package fakemgmt

import (
	"net/http"
	"sort"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// sandboxDeny is GET /v1/hosts/{id}/sandbox-deny, as mgmt serves it
// (mgmt/sandboxdeny.go): every tenant's credential file paths, sorted, for
// a registered host. Without it hostd fell back to GET /v1/tenants, the
// older-mgmt path (TestRouteDrift).
func (s *Server) sandboxDeny(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts[r.PathValue("id")] == nil {
		writeErr(w, 404, mgmtapi.CodeNotFound, "host not registered")
		return
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, t := range s.tenants {
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
