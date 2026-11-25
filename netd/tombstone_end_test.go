package netd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestCreateEndsTombstone (issue #364): an object ID killed with a
// tombstone can be created again before the tombstone ends (a workload
// deployed again right after `tw undeploy`). The old node must close then,
// and its tombstone timer must not remove the new node's state dir.
func TestCreateEndsTombstone(t *testing.T) {
	ctl := startControl(t)
	root := filepath.Join(newRundir(t), "state")
	reg, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: root, Keys: (&countingMint{}).source()})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o, _ := reg.Create("t", netd.ObjectOptions{})
	if err := o.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	const tomb = 3 * time.Second
	if _, err := reg.Kill(ctx, "t", netd.KillOptions{Reason: "undeployed", Tombstone: tomb}); err != nil {
		t.Fatal(err)
	}
	n, err := reg.Create("t", netd.ObjectOptions{})
	if err != nil || n == o {
		t.Fatalf("create during the tombstone: %v (same object: %v)", err, n == o)
	}
	if err := n.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(tomb + time.Second) // past the old tombstone's end
	if _, err := os.Stat(filepath.Join(root, "t")); err != nil {
		t.Fatalf("the new node's state dir is gone after the old tombstone ended: %v", err)
	}
	if n.State() != netd.StateReady {
		t.Fatalf("new node: %s", n.State())
	}
}
