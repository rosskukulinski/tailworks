package ui

import (
	"strings"
	"testing"
)

// TestLocalNodeIsOperator: mgmt's own node (the CLI via the loopback proxy
// or the -local-dev socket) gets the operator view because whoami reports
// it as an admin; the UI has no special case for kind "local".
func TestLocalNodeIsOperator(t *testing.T) {
	f := populatedAPI()
	f.routes["GET /v1/whoami"] = map[string]any{"kind": "local", "admin": true, "actor": "local", "mgmt": map[string]any{"dnsName": "tw-mgmt.t.ts.net", "tailnet": "t.ts.net"}}
	w := get(t, serve(t, f), "/ui/")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !f.called("GET /v1/status") || !f.called("GET /v1/hosts") {
		t.Fatalf("operator view not rendered; calls %v", f.calls)
	}
	if !strings.Contains(w.Body.String(), "tw-host-h1") && !strings.Contains(w.Body.String(), ">h1<") {
		t.Fatalf("hosts not listed:\n%s", w.Body)
	}
}
