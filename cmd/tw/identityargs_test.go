package main

import (
	"encoding/json"
	"slices"
	"testing"
)

// tw up's fast-identity flags reach the children, and state written
// before they existed keeps the binaries' defaults (never "0 = off").
func TestIdentityArgs(t *testing.T) {
	t.Parallel()
	zero, five, two := 0, 5, 2
	for _, tc := range []struct {
		name        string
		cfg         Config
		mgmt, hostd []string
	}{
		{"old state (no fields)", Config{}, nil, nil},
		{"defaults", Config{WarmKeys: &five, PoolNodes: &two}, []string{"-warm-keys", "5"}, []string{"-pool-nodes", "2"}},
		{"all off", Config{WarmKeys: &zero, PoolNodes: &zero}, []string{"-warm-keys", "0"}, []string{"-pool-nodes", "0"}},
		{"port mapping on", Config{PortMapping: true}, nil, []string{"-port-mapping"}},
	} {
		if got := tc.cfg.identityMgmtArgs(); !slices.Equal(got, tc.mgmt) {
			t.Errorf("%s: mgmt args %v, want %v", tc.name, got, tc.mgmt)
		}
		if got := tc.cfg.identityHostdArgs(); !slices.Equal(got, tc.hostd) {
			t.Errorf("%s: hostd args %v, want %v", tc.name, got, tc.hostd)
		}
	}
	// A state file from before these flags: the fields stay nil.
	var c Config
	if err := json.Unmarshal([]byte(`{"tailnet":"t.ts.net","hostID":"h"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.WarmKeys != nil || c.PoolNodes != nil || c.PortMapping {
		t.Fatalf("old state decoded as %+v", c)
	}
	// And a saved 0 survives the round trip (omitempty on a pointer keeps it).
	b, _ := json.Marshal(Config{WarmKeys: &zero, PoolNodes: &zero})
	var back Config
	json.Unmarshal(b, &back)
	if back.WarmKeys == nil || *back.WarmKeys != 0 || back.PoolNodes == nil || *back.PoolNodes != 0 {
		t.Fatalf("an explicit 0 did not survive the state file: %s", b)
	}
}
