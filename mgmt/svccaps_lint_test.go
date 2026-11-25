package mgmt

import (
	"strings"
	"testing"
)

func TestServiceCapWarnings(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"grants": [
		// caps on the tag only: calls through svc:web lose them
		{"src": ["tag:agent"], "dst": ["tag:tw-wl-web"], "ip": ["tcp:80"], "app": {"example.com/cap/mcp": [{"tools": ["a"]}]}},
		// the same caller's other cap is on the Service too: fine
		{"src": ["tag:agent"], "dst": ["tag:tw-wl-web"], "app": {"example.com/cap/x": [{}]}},
		{"src": ["tag:agent"], "dst": ["svc:web"], "ip": ["tcp:80"], "app": {"example.com/cap/x": [{}]}},
		// a grant to some other tag is not about this workload
		{"src": ["tag:agent"], "dst": ["tag:other"], "app": {"example.com/cap/y": [{}]}},
		// network-only tag grant: no caps to lose
		{"src": ["autogroup:member"], "dst": ["tag:tw-wl-web"], "ip": ["tcp:80"]},
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	w := serviceCapWarnings(pol, "svc:web", []string{"tag:tw-wl-web"})
	if len(w) != 1 {
		t.Fatalf("warnings = %q, want one", w)
	}
	for _, want := range []string{"tag:agent", "example.com/cap/mcp", "svc:web",
		`{"app":{"example.com/cap/mcp":[{"tools":["a"]}]},"dst":["svc:web"],"ip":["tcp:80"],"src":["tag:agent"]}`} {
		if !strings.Contains(w[0], want) {
			t.Errorf("warning %q lacks %q", w[0], want)
		}
	}
	if w := serviceCapWarnings(pol, "", []string{"tag:tw-wl-web"}); w != nil {
		t.Errorf("no Service: %q", w)
	}
}
