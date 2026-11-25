package netd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration/testcontrol"
)

// scopePeers makes s give every node except the one named seeAll a netmap
// holding only seeAll, as a policy whose grants keep objects from seeing one
// another would (RFC §9); seeAll sees every node. testcontrol has no
// visibility rules of its own, so this takes over its map stream: a
// non-streaming poll stores the node's endpoints, disco key and Hostinfo
// (what serveMap would), and a streaming poll re-sends the filtered map
// whenever it changes, polling once a second. It also drops nettest's
// rename hook, which would race these writes; the benchmark renames nothing.
// Benchmark-only: one map per second per node is plenty for an idle
// netmap.
func scopePeers(s *testcontrol.Server, seeAll string) {
	s.SetOnMapRequest(nil)
	s.AltMapStream = func(ctx context.Context, w testcontrol.MapStreamWriter, req *tailcfg.MapRequest) {
		if !req.Stream {
			if n := s.Node(req.NodeKey); n != nil {
				n.Endpoints, n.DiscoKey, n.Cap = req.Endpoints, req.DiscoKey, req.Version
				if req.Hostinfo != nil {
					n.Hostinfo = req.Hostinfo.View()
					if ni := n.Hostinfo.NetInfo(); ni.Valid() && ni.PreferredDERP() != 0 {
						n.HomeDERP = ni.PreferredDERP()
					}
				}
				s.UpdateNode(n)
			}
			w.SendMapMessage(&tailcfg.MapResponse{})
			return
		}
		var last []byte
		sent := time.Now()
		for {
			res, err := s.MapResponse(req)
			if err != nil || res == nil {
				return
			}
			if res.Node.Hostinfo.Hostname() != seeAll {
				var keep []*tailcfg.Node
				for _, p := range res.Peers {
					if p.Hostinfo.Valid() && p.Hostinfo.Hostname() == seeAll {
						keep = append(keep, p)
					}
				}
				res.Peers = keep
			}
			b, _ := json.Marshal(res)
			switch {
			case !bytes.Equal(b, last):
				if w.SendMapMessage(res) != nil {
					return
				}
				last, sent = b, time.Now()
			case time.Since(sent) > 30*time.Second:
				if w.SendMapMessage(&tailcfg.MapResponse{KeepAlive: true}) != nil {
					return
				}
				sent = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}
