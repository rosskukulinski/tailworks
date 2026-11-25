package mgmtd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestStartCloseBudget (#245) pins how long mgmt takes to start and stop
// on its own tailnet node (testcontrol): the node `tw up` and a service
// restart or reboot start, and `tw down` stops.
//
//   - Start used to wait for the node's state Running: its first netcheck
//     and a live DERP connection. That took ~3 s with STUN unanswered (a
//     Linux VM, a boot before the network is up) although mgmt can serve
//     as soon as it is logged in with a netmap. Both starts below have no
//     DERP or STUN at all; the restart took 3.1 s before, 0.1–0.3 s now.
//   - Close must not wait out tsnet.Server.Close's 5 s LocalAPI grace for
//     a request still in flight (the HTTPS certificate prefetch did, on
//     every `tw down` right after a start: e2e/twupgrade TestUpDownBudget
//     pins that one, as testcontrol answers certificate requests at once).
//
// The budgets are several times what a laptop takes, so a slow runner
// doesn't flake, and below the old numbers.
func TestStartCloseBudget(t *testing.T) {
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	// No DERP or STUN: nothing listens on port 9 (a boot before the
	// network is up). Set before any node talks to control, which reads
	// the map on every map response.
	for _, r := range ctl.Server.DERPMap.Regions {
		for _, n := range r.Nodes {
			n.DERPPort, n.STUNPort = 9, 9
		}
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "mgmt.key")
	if err := os.WriteFile(keyFile, []byte("TS_AUTHKEY="+nettest.DummyKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// As tw up starts it: the key is a single-use handoff (joinkey.go), so
	// the restart runs with the same options and no file.
	opts := Options{StateDir: filepath.Join(dir, "mgmt"), TSNetHostname: "tw-budget-mgmt", TSNetControlURL: ctl.URL,
		TSNetAuthKeyFile: keyFile, TSNetAuthKeyOnce: true, TSNetTLS: true, RegistryDir: "off", Version: "test"}
	for _, p := range []struct {
		name        string
		start, stop time.Duration
	}{
		{name: "first start (join)", start: 10 * time.Second, stop: 2500 * time.Millisecond},
		{name: "restart (saved node state)", start: 2 * time.Second, stop: 2500 * time.Millisecond},
	} {
		st := time.Now()
		rt, err := Start(context.Background(), opts)
		if err != nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		started := time.Since(st)
		if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
			t.Errorf("%s: the single-use key file is still there (stat: %v)", p.name, err)
		}
		st = time.Now()
		rt.Close()
		closed := time.Since(st)
		t.Logf("%s: Start %v, Close %v (https %v)", p.name, started.Round(time.Millisecond), closed.Round(time.Millisecond), rt.Self.HTTPS)
		if started > p.start {
			t.Errorf("%s: Start took %v (budget %v): waiting for netcheck and DERP?", p.name, started, p.start)
		}
		if closed > p.stop {
			t.Errorf("%s: Close took %v (budget %v): a LocalAPI request held tsnet.Server.Close?", p.name, closed, p.stop)
		}
	}
}
