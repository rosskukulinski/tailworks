//go:build e2ereal

// H22 (security/LEDGER.md, issue #155), on a real tailnet: which app
// capabilities does the Service host see for a caller that reaches it
// through the Service VIP, compared with the node's own address?
//
//	make e2e-svc-caps
//
// The tailnet policy needs (the caller is tag:ttest, the host is
// tag:ttest-rt-svch, and no grant between those two tags carries the
// Service cap); the test adds what is missing itself (real_caps_policy_test.go:
// additive, validated, If-Match):
//
//	"tagOwners":     {"tag:ttest-rt-svch": ["autogroup:admin"]},
//	"autoApprovers": {"services": {"svc:ttest-rt-caps": ["tag:ttest-rt-svch"]}},
//	"grants": [
//	  {"src": ["tag:ttest"], "dst": ["svc:ttest-rt-caps"], "ip": ["tcp:80"],
//	   "app": {"github.com/rosskukulinski/tailworks/cap/ttest-rt-svc": [{"via": "svc"}]}},
//	  {"src": ["tag:ttest"], "dst": ["tag:ttest-rt-svch"], "ip": ["tcp:80"],
//	   "app": {"github.com/rosskukulinski/tailworks/cap/ttest-rt-node": [{"via": "node"}]}},
//	]
//
// The second grant is the node-destination comparison. Without it the test
// still runs the Service half (TW_E2E_SVC_ONLY=1 says it was removed on
// purpose).
package tssvc_test

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/internal/tssvc"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

const (
	capsSvc     = "svc:ttest-rt-caps"
	capsHostTag = "tag:ttest-rt-svch"
	capViaSvc   = "github.com/rosskukulinski/tailworks/cap/ttest-rt-svc"
	capViaNode  = "github.com/rosskukulinski/tailworks/cap/ttest-rt-node"
)

// capsSeen is what the host's backend reports for one request.
type capsSeen struct {
	Via    string   `json:"via"`    // "service" or "node"
	Caller string   `json:"caller"` // WhoIs node ID
	Local  string   `json:"local"`  // the conn's local address (the VIP on Service flows)
	WhoIs  []string `json:"whois"`  // cap names from plain WhoIs (what netd uses today)
	ForDst []string `json:"forDst"` // cap names from WhoIsForIP(remote, local addr)
	ForSvc []string `json:"forSvc"` // cap names from WhoIsForService (Service flows only)
	TW     []string `json:"tw"`     // cap names from tssvc.WhoIs (what netd and mgmt use)
}

func capNames(m tailcfg.PeerCapMap) []string {
	out := []string{}
	for k := range m {
		if k == capViaSvc || k == capViaNode {
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return out
}

func TestRealServiceCaps(t *testing.T) {
	keyFile := os.Getenv("TW_E2E_API_KEY_FILE")
	if keyFile == "" {
		t.Skip("TW_E2E_API_KEY_FILE not set")
	}
	// Only the test tailnets' credentials (TS_CREDS/tailnet{1,2}_api.env).
	if b := filepath.Base(keyFile); b != "tailnet1_api.env" && b != "tailnet2_api.env" {
		t.Fatalf("refusing to run with credentials %q (not a test tailnet's)", b)
	}
	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	// The key's own tailnet ("-"); the MagicDNS name comes from the netmap.
	api := &tsapi.Client{Tailnet: cmp.Or(os.Getenv("TW_E2E_TAILNET"), "-"), APIKey: apiKey}
	tracker := tsapi.NewTracker(api)
	b := make([]byte, 3)
	rand.Read(b)
	run := hex.EncodeToString(b)
	prefix := "ttest-rt-caps-" + run
	applyCapsPolicy(t, ctx, api, keyFile, run)

	def, err := api.PutService(ctx, tsapi.Service{Name: capsSvc, Comment: "tw H22 e2e " + run, Ports: []string{"tcp:80"}})
	if err != nil {
		t.Fatalf("put service: %v", err)
	}
	t.Cleanup(func() {
		cctx, cc := context.WithTimeout(context.Background(), time.Minute)
		defer cc()
		if err := api.DeleteService(cctx, capsSvc); err != nil {
			t.Errorf("delete service: %v", err)
		}
		t.Logf("cleanup: %+v", tracker.Cleanup(cctx, prefix))
	})

	node := func(name, tag string) *tsnet.Server {
		k, err := api.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{tag}, Ephemeral: true, Preauthorized: true, ExpirySeconds: 600, Description: "tw H22 e2e"})
		if err != nil {
			t.Fatalf("mint %s: %v", tag, err)
		}
		t.Cleanup(func() { api.DeleteKey(context.Background(), k.ID) })
		s := &tsnet.Server{Hostname: name, Dir: t.TempDir(), AuthKey: k.Secret, Ephemeral: true,
			ControlURL: "https://controlplane.tailscale.com", Logf: func(string, ...any) {}}
		st, err := s.Up(ctx)
		if err != nil {
			t.Fatalf("up %s: %v", name, err)
		}
		tracker.Track(string(st.Self.ID), name)
		t.Cleanup(func() { s.Close() })
		return s
	}
	host := node(prefix+"-h", capsHostTag)
	client := node(prefix+"-c", "tag:ttest")
	hlc, _ := host.LocalClient()
	hlc.OmitAuth = true
	clc, _ := client.LocalClient()
	cst, _ := clc.StatusWithoutPeers(ctx)
	tailnet := cst.CurrentTailnet.MagicDNSSuffix
	hip4, _ := host.TailscaleIPs()

	serve := func(ln net.Listener, via string) {
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wi, err := hlc.WhoIs(r.Context(), r.RemoteAddr)
			if err != nil {
				http.Error(w, "whois: "+err.Error(), 500)
				return
			}
			local := r.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
			out := capsSeen{Via: via, Caller: string(wi.Node.StableID), Local: local, WhoIs: capNames(wi.CapMap)}
			if lap, err := netip.ParseAddrPort(local); err == nil {
				if fd, err := hlc.WhoIsForIP(r.Context(), r.RemoteAddr, lap.Addr()); err == nil {
					out.ForDst = capNames(fd.CapMap)
				}
			}
			if via == "service" {
				if fs, err := hlc.WhoIsForService(r.Context(), r.RemoteAddr, capsSvc); err == nil {
					out.ForSvc = capNames(fs.CapMap)
				}
			}
			if tw, err := tssvc.WhoIs(r.Context(), hlc, r.RemoteAddr); err == nil {
				out.TW = capNames(tw.CapMap)
			}
			json.NewEncoder(w).Encode(out)
		}))
	}
	own, err := host.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	serve(own, "node")
	h := tssvc.NewHost(host, hlc)
	defer h.Close(context.Background())
	cl := tssvc.NewConnListener(tssvc.Addr{Name: capsSvc})
	h.Route(80, cl.Deliver)
	serve(cl, "service")
	if err := h.Advertise(ctx, capsSvc, 80); err != nil {
		t.Fatalf("advertise: %v", err)
	}

	hc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: client.Dial, DisableKeepAlives: true}}
	get := func(u string) (capsSeen, error) {
		var s capsSeen
		resp, err := hc.Get(u)
		if err != nil {
			return s, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return s, fmt.Errorf("%d %s", resp.StatusCode, body)
		}
		return s, json.Unmarshal(body, &s)
	}
	waitGet := func(u string, d time.Duration) capsSeen {
		t0 := time.Now()
		var lastErr error
		for time.Since(t0) < d {
			s, err := get(u)
			if err == nil {
				return s
			}
			lastErr = err
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("GET %s: timed out after %v: %v", u, d, lastErr)
		return capsSeen{}
	}

	svc := waitGet("http://"+tssvc.Label(capsSvc)+"."+tailnet+"/", 3*time.Minute)
	t.Logf("through the Service VIP: %+v", svc)
	if svc.Via != "service" || svc.Caller != string(cst.Self.ID) {
		t.Fatalf("not a Service flow from the client: %+v (client %s)", svc, cst.Self.ID)
	}
	// MagicDNS dials the IPv6 VIP; check the IPv4 VIP too.
	for _, a := range def.Addrs {
		if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() {
			v4 := waitGet("http://"+a+"/", time.Minute)
			t.Logf("through the IPv4 VIP: %+v", v4)
			if v4.Via != "service" || !slices.Equal(v4.TW, []string{capViaSvc}) {
				t.Errorf("IPv4 VIP: %+v, want a Service flow with caps [%s]", v4, capViaSvc)
			}
		}
	}

	svcOnly := os.Getenv("TW_E2E_SVC_ONLY") == "1"
	var nd capsSeen
	if !svcOnly {
		nd = waitGet("http://"+hip4.String()+"/", time.Minute)
		t.Logf("to the node's own address: %+v", nd)
		if nd.Via != "node" || nd.Caller != string(cst.Self.ID) {
			t.Fatalf("not a node flow from the client: %+v", nd)
		}
	}

	// What the policy reads: the svc: grant applies to flows to the VIP, the
	// tag grant to flows to the node's own address. Scoped WhoIs
	// (WhoIsForIP on the conn's local address) gives exactly that.
	want := func(what string, got, exp []string) {
		t.Helper()
		if !slices.Equal(got, exp) {
			t.Errorf("%s caps = %v, want %v", what, got, exp)
		}
	}
	want("Service flow, WhoIsForIP(VIP)", svc.ForDst, []string{capViaSvc})
	want("Service flow, WhoIsForService", svc.ForSvc, []string{capViaSvc})
	want("Service flow, tssvc.WhoIs", svc.TW, []string{capViaSvc})
	// Plain WhoIs scopes caps to the node's own address, whatever the flow's
	// destination: it misses the svc: grant and reports the node grant on a
	// Service flow.
	wantPlain := []string{capViaNode}
	if svcOnly {
		wantPlain = []string{}
	}
	want("Service flow, plain WhoIs", svc.WhoIs, wantPlain)
	if !svcOnly {
		want("node flow, WhoIsForIP(node)", nd.ForDst, []string{capViaNode})
		want("node flow, plain WhoIs", nd.WhoIs, []string{capViaNode})
		want("node flow, tssvc.WhoIs", nd.TW, []string{capViaNode})
	}
}
