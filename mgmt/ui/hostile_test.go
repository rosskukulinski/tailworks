package ui

import (
	"strings"
	"testing"
)

// TestHostileStringsEscaped is the admin UI's half of ledger H13 (the CLI's
// is attacks.TestRT3_H13_TerminalInjection): an object name, a deployment
// error and a public request path are tenant- or internet-controlled, and
// html/template must escape them on every page that shows them. Control
// characters are inert in HTML; markup is what matters here.
func TestHostileStringsEscaped(t *testing.T) {
	const evil = `"><script>alert(1)</script><img src=x onerror=alert(2)>`
	f := populatedAPI()
	objs := f.routes["GET /v1/objects"].(map[string]any)["items"].([]any)
	kid := objs[1].(map[string]any)
	kid["objectName"] = "kid" + evil + "\x1b[2J"
	f.routes["GET /v1/objects/obj_kid"] = kid
	dep := f.routes["GET /v1/deployments/dep_1"].(map[string]any)
	dep["error"] = "boom" + evil
	au := f.routes["GET /v1/audit"].(map[string]any)
	au["items"] = append(au["items"].([]any), map[string]any{"seq": 8, "chain": "host:h1", "ts": "2026-11-21T00:00:00Z", "tenantID": "tn_1", "objectID": "obj_kid",
		"kind": "public_request", "source": "host", "detail": map[string]any{"method": "GET", "path": "/webhooks/" + evil + "\x1b[31m\r\nFAKE", "status": 200}, "prevHash": "", "hash": "h"})
	h := serve(t, f)
	for _, p := range pages {
		body := get(t, h, p).Body.String()
		for _, bad := range []string{"<script>alert", "<img src=x", `"><script`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: hostile markup %q rendered unescaped", p, bad)
			}
		}
	}
	for _, p := range []string{"/ui/objects/obj_kid", "/ui/audit"} {
		if body := get(t, h, p).Body.String(); !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: the hostile string is not shown escaped", p)
		}
	}
}
