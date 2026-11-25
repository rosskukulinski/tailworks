package mgmt

import (
	"context"
	"testing"
)

func TestSplitConnect(t *testing.T) {
	t.Parallel()
	for in, want := range map[string][2]string{
		"tag:db":                 {"tag:db", ""},
		"tag:db:5432":            {"tag:db", "tcp:5432"},
		"*":                      {"*", ""},
		"autogroup:member:22":    {"autogroup:member", "tcp:22"},
		"db.example.ts.net:5432": {"db.example.ts.net", "tcp:5432"},
		"db":                     {"db", ""},
		"tag:db:0":               {"tag:db:0", ""},
		"tag:db:99999":           {"tag:db:99999", ""},
		"tag:db:x":               {"tag:db:x", ""},
	} {
		if tg, ip := SplitConnect(in); tg != want[0] || ip != want[1] {
			t.Errorf("SplitConnect(%q) = %q, %q; want %q, %q", in, tg, ip, want[0], want[1])
		}
	}
}

func TestHasIPGrantPort(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"grants": [
		{"src": ["tag:a"], "dst": ["tag:db"], "ip": ["tcp:5432"]},
		{"src": ["tag:b"], "dst": ["tag:db"], "ip": ["tcp:80"]},
		{"src": ["tag:c"], "dst": ["tag:db"], "ip": ["*"]},
		{"src": ["tag:d"], "dst": ["tag:db"], "ip": ["tcp:5000-6000"]},
		{"src": ["tag:e"], "dst": ["*"], "ip": ["5432"]},
		{"src": ["tag:f"], "dst": ["tag:db"], "ip": ["udp:5432"]},
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]bool{"tag:a": true, "tag:b": false, "tag:c": true, "tag:d": true, "tag:e": true, "tag:f": false, "tag:z": false} {
		if got := pol.HasIPGrantPort(src, "tag:db", "tcp:5432"); got != want {
			t.Errorf("%s → tag:db tcp:5432: %v, want %v", src, got, want)
		}
	}
	if !pol.HasIPGrantPort("tag:b", "tag:db", "") {
		t.Error("no port: any ip grant counts (as before)")
	}
}

// TestPreflightConnectsPort: a connects entry with a port is checked
// against that port, and the missing grant's snippet names it.
func TestPreflightConnectsPort(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = `{"tagOwners": {"tag:tw-wl-app": ["autogroup:admin"]}, "grants": [
		{"src": ["tag:tw-wl-app"], "dst": ["tag:db"], "ip": ["tcp:80"]}]}`
	tn := v1Tenant(t, e, "t.ts.net", nil)
	res, err := e.m.Preflight(context.Background(), tn, PreflightRequest{SrcTags: []string{"tag:tw-wl-app"}, Connects: []string{"tag:db:5432", "tag:web"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 2 || res.Missing[0].Dst != "tag:db" || res.Missing[0].Snippet != `{ "src": ["tag:tw-wl-app"], "dst": ["tag:db"], "ip": ["tcp:5432"] }` ||
		res.Missing[1].Dst != "tag:web" || res.Missing[1].Snippet != `{ "src": ["tag:tw-wl-app"], "dst": ["tag:web"], "ip": ["tcp:80"] }` {
		t.Fatalf("%+v", res.Missing)
	}
}
