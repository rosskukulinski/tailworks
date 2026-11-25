package hostdrun

import (
	"context"
	"strings"
	"testing"
)

func TestParseInternetMode(t *testing.T) {
	for in, want := range map[string][2]string{
		"direct":                     {InternetDirect, ""},
		" OFF ":                      {InternetOff, ""},
		"exit-node":                  {InternetExitNode, ""},
		"exit-node=exit-fra":         {InternetExitNode, "exit-fra"},
		"Exit-Node=nABC,dns=1.1.1.1": {InternetExitNode, "nABC,dns=1.1.1.1"}, // the node keeps its case
	} {
		kind, spec, err := ParseInternetMode(in)
		if err != nil || kind != want[0] || spec != want[1] {
			t.Errorf("ParseInternetMode(%q) = %q %q %v, want %v", in, kind, spec, err, want)
		}
	}
	for _, bad := range []string{"", "on", "direct=x", "off=", "exit-node=", "exit-node=,dns=1.1.1.1", "exit-node=x,dns=nope"} {
		if _, _, err := ParseInternetMode(bad); err == nil {
			t.Errorf("ParseInternetMode(%q): no error", bad)
		}
	}
}

// TestInternetPaths: direct/off build (or withhold) the direct path; an
// exit node needs the host's own node, one node, and no direct path
// beside it.
func TestInternetPaths(t *testing.T) {
	ctx := context.Background()
	wk, ct, exit, err := internetPaths(ctx, "direct", "off", nil, nil)
	if err != nil || wk == nil || ct != nil || exit != nil {
		t.Fatalf("direct/off: %v %v %v %v", wk, ct, exit, err)
	}
	if wk, ct, _, err := internetPaths(ctx, "off", "direct", nil, nil); err != nil || wk != nil || ct == nil {
		t.Fatalf("off/direct: %v %v %v", wk, ct, err)
	}
	for _, c := range []struct{ worker, container, want string }{
		{"exit-node=a", "direct", "one internet path"},
		{"direct", "exit-node=a", "one internet path"},
		{"exit-node=a", "exit-node=b", "one exit node"},
		{"exit-node", "exit-node", "needs a node"},
		{"exit-node", "off", "needs a node"},
		{"exit-node=a", "exit-node", "own tailnet node"}, // valid, but no node here
		{"exit-node=a", "off", "own tailnet node"},
		{"off", "exit-node=a,dns=1.1.1.1", "own tailnet node"},
		{"bogus", "off", "-worker-internet"},
		{"off", "bogus", "-container-internet"},
	} {
		_, _, _, err := internetPaths(ctx, c.worker, c.container, nil, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s/%s: %v, want %q", c.worker, c.container, err, c.want)
		}
	}
}
