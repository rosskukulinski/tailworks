package netd_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/nodecap"
)

// waitNetmapCache waits until tailscale has written dir's netmap cache
// (profile-data/<profile>/netmap-cache), which it does a moment after the
// node's first netmap.
func waitNetmapCache(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if m, _ := filepath.Glob(filepath.Join(dir, "profile-data", "*", "netmap-cache", "*")); len(m) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no netmap cache under %s", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRejoinFromNetmapCacheRejectedRemints (issue #200): a node that rejoins
// from saved state WITH tailscale's netmap cache goes Running from the
// cached netmap before control has answered, so it is Ready before control
// can reject it. Control then expires it (NeedsLogin after Ready). netd must
// notice that after Ready too: take the dead node out of service, drop its
// state, call OnRejected, and the next Create re-mints under the same
// hostname (KeyRequest.Remint). The persistent and the logout-lost
// ephemeral cases of TestRejoinExpiredStillLoggedInRemints, cache kept.
func TestRejoinFromNetmapCacheRejectedRemints(t *testing.T) {
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
			// Production control turns the netmap cache on (nodeAttr
			// cache-network-maps); testcontrol doesn't, so ask for it.
			st, err := o.LocalClient().StatusWithoutPeers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ctl.Server.SetNodeCapMap(st.Self.PublicKey, tailcfg.NodeCapMap{nodecap.CacheNetworkMaps: nil})
			dir := filepath.Join(root, "p")
			waitNetmapCache(t, dir)
			statePath := filepath.Join(dir, "tailscaled.state")
			saved, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			reg.Close()
			if err := os.WriteFile(statePath, saved, 0o600); err != nil {
				t.Fatal(err)
			}
			ctl.Server.SetExpireAllNodes(true)
			var remints, plain atomic.Int32
			cfg.Keys = netd.MintFunc(func(ctx context.Context, req netd.KeyRequest) (string, error) {
				if req.Remint {
					remints.Add(1)
				} else {
					plain.Add(1)
				}
				ctl.Server.SetExpireAllNodes(false)
				return nettest.DummyKey, nil
			})
			var reg2 *netd.Registry
			rejected := make(chan string, 4)
			cfg.OnRejected = func(id string) {
				reg2.Create(id, opts) // the owner restarts the node (hostd: startNodeLocked)
				rejected <- id
			}
			reg2, err = netd.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reg2.Close()
			if _, err := reg2.Rejoin(); err != nil {
				t.Fatal(err)
			}
			t0 := time.Now()
			// Detection takes needsLoginGrace (2 s) whether it happens
			// before Ready (no cache) or after it (the cache); 20 s is ample
			// under load.
			deadline := t0.Add(20 * time.Second)
			var in netd.ObjectInfo
			for {
				if o := reg2.Get("p"); o != nil {
					in = o.Info()
					if in.State == netd.StateReady && in.Reminted {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("a rejoin from the netmap cache that control rejected was never re-minted: %+v (before node %s)", in, before.NodeID)
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Logf("reminted and ready %v after Rejoin", time.Since(t0).Round(time.Millisecond))
			if in.Hostname != before.Hostname || in.NodeID == before.NodeID || in.Rejoined || remints.Load() != 1 || plain.Load() != 0 {
				t.Errorf("after: %+v (before node %s), %d remints, %d plain mints", in, before.NodeID, remints.Load(), plain.Load())
			}
			select {
			case id := <-rejected:
				if id != "p" {
					t.Errorf("OnRejected(%q)", id)
				}
			default:
				// Usually the node is Running from the cache before
				// control answers, so the rejection comes after Ready.
				// Now and then (seen on the mbp runner, ephemeral case)
				// control's answer wins and up() re-mints it itself.
				t.Log("rejected before Ready; up() re-minted it")
			}
		})
	}
}

// TestRejectedAfterKillStaysKilled (issue #200): a rejection that lands
// after a kill must not bring the node back. A tombstoned node stays up
// after Kill (mgmt deletes its device meanwhile, which control reports as
// NeedsLogin); watchRejected must leave it killed and never call
// OnRejected.
func TestRejectedAfterKillStaysKilled(t *testing.T) {
	ctl := startControl(t)
	var rejected atomic.Int32
	reg, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: filepath.Join(newRundir(t), "state"),
		Keys: (&countingMint{}).source(), OnRejected: func(string) { rejected.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o, _ := reg.Create("k", netd.ObjectOptions{})
	if err := o.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Kill(ctx, "k", netd.KillOptions{Reason: "test", Tombstone: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	ctl.Server.SetExpireAllNodes(true)
	time.Sleep(4 * time.Second) // twice needsLoginGrace
	if n := rejected.Load(); n != 0 || o.State() != netd.StateKilled {
		t.Fatalf("after a kill: state %s, OnRejected called %d times", o.State(), n)
	}
}

// TestReadyNodeRejectedLater (issue #200): control can also reject a node
// that has been up for a while (its key expired, or the device was
// removed). netd fails it and calls OnRejected; Create re-mints it.
func TestReadyNodeRejectedLater(t *testing.T) {
	ctl := startControl(t)
	var reg *netd.Registry
	rejected := make(chan string, 4)
	var remints atomic.Int32
	reg, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: filepath.Join(newRundir(t), "state"),
		Keys: netd.MintFunc(func(ctx context.Context, req netd.KeyRequest) (string, error) {
			if req.Remint {
				remints.Add(1)
				ctl.Server.SetExpireAllNodes(false)
			}
			return nettest.DummyKey, nil
		}),
		// Re-create before signalling: the test reads reg.Get as soon as
		// it hears of the rejection, and must see the new object, not the
		// failed one (sending first raced; CI saw bootstrap_failed).
		OnRejected: func(id string) { reg.Create(id, netd.ObjectOptions{}); rejected <- id }})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o, _ := reg.Create("l", netd.ObjectOptions{})
	if err := o.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	before := o.Info()
	ctl.Server.SetExpireAllNodes(true)
	select {
	case <-rejected:
	case <-time.After(20 * time.Second):
		t.Fatalf("a ready node whose key expired was not rejected: %+v", o.Info())
	}
	if in := o.Info(); in.State != netd.StateFailed {
		t.Errorf("the rejected node: %+v", in)
	}
	o2 := reg.Get("l")
	if o2 == o {
		t.Fatal("OnRejected signalled before the object was re-created")
	}
	if err := o2.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if in := o2.Info(); !in.Reminted || in.NodeID == before.NodeID || in.Hostname != before.Hostname || remints.Load() != 1 {
		t.Errorf("after: %+v (before node %s), %d remints", in, before.NodeID, remints.Load())
	}
}
