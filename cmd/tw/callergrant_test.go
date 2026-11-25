package main

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

func TestCallerGrant(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tags  []string
		https bool
		want  string
	}{
		{nil, false, ""},
		{[]string{"tag:tw-wl-support"}, false, `{ "src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"] }`},
		{[]string{"tag:a", "tag:b"}, false, `{ "src": ["autogroup:member"], "dst": ["tag:a", "tag:b"], "ip": ["tcp:80"] }`},
		// tailnet.https (#360): the node serves 443 too.
		{[]string{"tag:tw-wl-mcp"}, true, `{ "src": ["autogroup:member"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80", "tcp:443"] }`},
	} {
		if got := callerGrant(c.tags, c.https); got != c.want {
			t.Errorf("callerGrant(%q)\n got %s\nwant %s", c.tags, got, c.want)
		}
	}
}

// TestServiceCallerGrant (#155): a workload behind a Service also gets the
// grant to its svc: name, where callers' app caps must go.
func TestServiceCallerGrant(t *testing.T) {
	t.Parallel()
	if got := serviceCallerGrant(""); got != "" {
		t.Errorf("no Service: %q", got)
	}
	want := `{ "src": ["autogroup:member"], "dst": ["svc:support"], "ip": ["tcp:80"] }`
	if got := serviceCallerGrant("svc:support"); got != want {
		t.Errorf("serviceCallerGrant\n got %s\nwant %s", got, want)
	}
	pf := mgmt.PreflightResult{Service: "svc:support"}
	if s := policySnippet(pf, callerGrantEntry([]string{"tag:tw-wl-support"}, false), serviceCallerGrantEntry("svc:support")); !strings.Contains(s, want+", // calls through svc:support") {
		t.Errorf("policy snippet lacks the Service grant:\n%s", s)
	}
}
