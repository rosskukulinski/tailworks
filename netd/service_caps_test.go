package netd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tssvc"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestServiceCapsScopedToVIP (H22, issue #155): the caps a workload sees on
// a flow through its Service VIP are the grants whose destination is the
// Service, not the grants to the node's own address, and the other way
// round on the node's address. This is how production control behaves for
// `svc:` grants (TestRealServiceCaps, `make e2e-svc-caps`); testcontrol has
// no `svc:` grants, so the test sends the object node a packet filter with
// one cap granted to the VIP and one to the node's own addresses.
func TestServiceCapsScopedToVIP(t *testing.T) {
	e := startEnv(t, "a")
	ctx := context.Background()
	const svc, vip = "svc:capped", "100.99.0.12"
	const capSvc, capNode = "example.com/cap/h22-svc", "example.com/cap/h22-node"

	o := e.reg.Get("a")
	// netd already set OmitAuth on this client (writing it here again
	// races with its rejected-node watcher, #271).
	alc, err := o.Server().LocalClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := nettest.AcceptRoutes(ctx, e.client.LC); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.AdvertiseService(ctx, "a", svc); err != nil {
		t.Fatal(err)
	}
	if err := e.ctl.HostService(ctx, alc, svc, vip); err != nil {
		t.Fatal(err)
	}
	if err := nettest.WaitDNSRecord(ctx, e.client.LC, vip); err != nil {
		t.Fatal(err)
	}
	viaSvc := "http://" + tssvc.Label(svc) + "." + nettest.MagicDNSDomain + "/"
	viaNode := "http://" + e.objIP["a"] + "/"
	caps := func(u string) (map[string][]json.RawMessage, error) {
		code, b, err := tryGet(e.hc, u)
		if err != nil {
			return nil, err
		}
		if code != 200 {
			return nil, errString(fmt.Sprintf("%d %s", code, b))
		}
		var r objResp
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		if r.Caller == nil {
			return nil, errString("no caller")
		}
		return r.Caller.CapMap, nil
	}
	eventually(t, 20*time.Second, func() error { _, err := caps(viaSvc); return err })

	st, err := alc.StatusWithoutPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var own []netip.Prefix
	for _, a := range st.TailscaleIPs {
		own = append(own, netip.PrefixFrom(a, a.BitLen()))
	}
	if !e.ctl.Server.AddRawMapResponse(st.Self.PublicKey, &tailcfg.MapResponse{
		PacketFilters: map[string][]tailcfg.FilterRule{"h22": {{
			SrcIPs: []string{"*"},
			CapGrant: []tailcfg.CapGrant{
				{Dsts: []netip.Prefix{netip.MustParsePrefix(vip + "/32")}, CapMap: tailcfg.PeerCapMap{capSvc: {`{"via":"svc"}`}}},
				{Dsts: own, CapMap: tailcfg.PeerCapMap{capNode: {`{"via":"node"}`}}},
			},
		}}},
	}) {
		t.Fatal("object node has no map stream")
	}

	// The node's own address: the node grant (it also tells us the filter
	// has arrived), never the Service grant.
	var nodeCaps map[string][]json.RawMessage
	eventually(t, 20*time.Second, func() error {
		c, err := caps(viaNode)
		if err != nil {
			return err
		}
		if _, ok := c[capNode]; !ok {
			return errString(fmt.Sprintf("node flow: no %s yet: %v", capNode, c))
		}
		nodeCaps = c
		return nil
	})
	if _, ok := nodeCaps[capSvc]; ok {
		t.Errorf("node flow got the Service-only cap %s: %v", capSvc, nodeCaps)
	}
	if _, ok := nodeCaps[nettest.TestCap]; !ok {
		t.Errorf("node flow lost the tailnet-wide cap: %v", nodeCaps)
	}

	// Through the VIP: the Service grant, not the node grant.
	svcCaps, err := caps(viaSvc)
	if err != nil {
		t.Fatal(err)
	}
	if v := svcCaps[capSvc]; len(v) != 1 || string(v[0]) != `{"via":"svc"}` {
		t.Errorf("Service flow: %s = %s, want the svc: grant's value (caps %v)", capSvc, v, svcCaps)
	}
	if _, ok := svcCaps[capNode]; ok {
		t.Errorf("Service flow got the node-only cap %s: %v", capNode, svcCaps)
	}
	if _, ok := svcCaps[nettest.TestCap]; !ok {
		t.Errorf("Service flow lost the tailnet-wide cap: %v", svcCaps)
	}
}
