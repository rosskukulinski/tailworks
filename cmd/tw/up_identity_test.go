package main

import (
	"errors"
	"io"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/mgmtd"
)

// TestIdentitySingle: the single layout applies the saved --warm-keys,
// --pool-nodes and --port-mapping (#457): unset is the daemons' default,
// an explicit 0 is off.
func TestIdentitySingle(t *testing.T) {
	var mo mgmtd.Options
	var ho hostdrun.Options
	Config{}.identitySingle(&mo, &ho)
	if mo.WarmKeys != mgmtd.DefaultWarmKeys || ho.PoolNodes != hostdrun.DefaultPoolNodes || ho.PortMapping {
		t.Errorf("unset: warm %d pool %d portmap %v, want the defaults", mo.WarmKeys, ho.PoolNodes, ho.PortMapping)
	}
	zero, three := 0, 3
	Config{WarmKeys: &zero, PoolNodes: &three, PortMapping: true}.identitySingle(&mo, nil)
	Config{WarmKeys: &zero, PoolNodes: &three, PortMapping: true}.identitySingle(nil, &ho)
	if mo.WarmKeys != 0 || ho.PoolNodes != 3 || !ho.PortMapping {
		t.Errorf("saved: warm %d pool %d portmap %v, want 0 3 true", mo.WarmKeys, ho.PoolNodes, ho.PortMapping)
	}
}

// TestRemovedUpFlags: the four removed tuning flags still parse (scripts
// keep working) and defaultTuning fills only what tw.json leaves empty.
func TestRemovedUpFlags(t *testing.T) {
	fs := upFlagSet(t)
	if err := fs.Parse([]string{"--heartbeat", "2s", "--kill-tombstone", "3s", "--device-delete-delay", "1s", "--push-port", "7575"}); err != nil {
		t.Fatalf("removed flags no longer parse: %v", err)
	}
	c := Config{KillTombstone: "20s"}
	c.defaultTuning()
	if c.Heartbeat != "1s" || c.KillTombstone != "20s" || c.DeviceDeleteDelay != "8s" {
		t.Errorf("defaultTuning: %q %q %q, want 1s 20s (saved) 8s", c.Heartbeat, c.KillTombstone, c.DeviceDeleteDelay)
	}
}

// TestUpPreflightWorkerd: a workerd that does not run stops tw up before
// anything is minted (#480).
func TestUpPreflightWorkerd(t *testing.T) {
	var ce *cliErr
	if err := upPreflight("/nonexistent/workerd", io.Discard); !errors.As(err, &ce) || ce.Code != "workerd_broken" {
		t.Fatalf("broken workerd: %v, want workerd_broken", err)
	}
	wd, err := findWorkerd("")
	if err != nil {
		t.Skip("no workerd")
	}
	if err := upPreflight(wd, io.Discard); err != nil {
		t.Errorf("working workerd: %v", err)
	}
}
