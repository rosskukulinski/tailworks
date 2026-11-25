package netd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestRejoinExpiredStillLoggedInRemints (issue #182): a node that rejoins
// from saved state still marked logged in, and whose key control has
// expired, never reaches NeedsLogin. It loops in NoState on NodeKeyExpired
// login errors, which reach the IPN bus only as the login-state health
// warning. Without that signal the rejoin hung until JoinTimeout. That
// happens to a persistent node, and to an ephemeral one whose best-effort
// logout at shutdown didn't land (on a loaded CI runner,
// TestRejoinRejectedRemints's "gone"). The ephemeral case is simulated by
// restoring the node state saved before Close, which the logout rewrites.
func TestRejoinExpiredStillLoggedInRemints(t *testing.T) {
	eph := true
	for name, opts := range map[string]netd.ObjectOptions{"persistent": {}, "ephemeral-logout-lost": {Ephemeral: &eph}} {
		t.Run(name, func(t *testing.T) {
			ctl := startControl(t)
			root := filepath.Join(newRundir(t), "state")
			cfg := netd.Config{ControlURL: ctl.URL, StateRoot: root, Keys: (&countingMint{}).source()}
			reg, err := netd.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			o, _ := reg.Create("p", opts)
			if err := o.Ready(ctx); err != nil {
				t.Fatal(err)
			}
			before := o.Info()
			statePath := filepath.Join(root, "p", "tailscaled.state")
			saved, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			reg.Close()
			if err := os.WriteFile(statePath, saved, 0o600); err != nil {
				t.Fatal(err)
			}
			// Start without tailscale's netmap cache, which is written a
			// moment after the first netmap (so only sometimes before
			// Close). With it the backend is Running from the cached netmap
			// before control answers; that case (issue #200) is
			// TestRejoinFromNetmapCacheRejectedRemints.
			os.RemoveAll(filepath.Join(root, "p", "profile-data"))
			ctl.Server.SetExpireAllNodes(true)
			remints := 0
			cfg.Keys = netd.MintFunc(func(ctx context.Context, req netd.KeyRequest) (string, error) {
				if req.Remint {
					remints++
				}
				ctl.Server.SetExpireAllNodes(false)
				return nettest.DummyKey, nil
			})
			reg2, err := netd.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reg2.Close()
			if _, err := reg2.Rejoin(); err != nil {
				t.Fatal(err)
			}
			// Detection takes needsLoginGrace (2 s); 20 s is ample under load
			// and far below the 2 min JoinTimeout a hang would wait out.
			rctx, rcancel := context.WithTimeout(ctx, 20*time.Second)
			defer rcancel()
			st := time.Now()
			if err := reg2.Get("p").Ready(rctx); err != nil {
				t.Fatalf("rejoin with an expired key never ended: %v", err)
			}
			in := reg2.Get("p").Info()
			t.Logf("reminted and ready %v after Rejoin", time.Since(st).Round(time.Millisecond))
			if !in.Reminted || in.Hostname != before.Hostname || in.NodeID == before.NodeID || remints != 1 {
				t.Errorf("after: %+v (before node %s), %d remints", in, before.NodeID, remints)
			}
		})
	}
}
