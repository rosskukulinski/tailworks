package hostd

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/features"
	"github.com/rosskukulinski/tailworks/netd"
	"golang.org/x/net/dns/dnsmessage"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// dnsStubAnswer answers a wire-format query from a table of names.
func dnsStubAnswer(t *testing.T, q []byte, table map[string][]netip.Addr) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) != 1 {
		t.Fatalf("questions: %v %v", qs, err)
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true})
	b.StartQuestions()
	b.Question(qs[0])
	b.StartAnswers()
	for _, a := range table[strings.TrimSuffix(qs[0].Name.String(), ".")] {
		hdr := dnsmessage.ResourceHeader{Name: qs[0].Name, Class: dnsmessage.ClassINET, TTL: 60}
		if a.Is4() && qs[0].Type == dnsmessage.TypeA {
			b.AResource(hdr, dnsmessage.AResource{A: a.As4()})
		}
		if a.Is6() && qs[0].Type == dnsmessage.TypeAAAA {
			b.AAAAResource(hdr, dnsmessage.AAAAResource{AAAA: a.As16()})
		}
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestExitNodeInternet (ADR-054): the path selects the exit
// node on the host node, resolves through the exit node's peer API (or a
// dns= resolver) over the tailnet dialer, dials through it, and refuses
// with exit_node_unavailable, never touching anything else, when the exit
// node is missing, not offered, offline or refuses DNS.
func TestExitNodeInternet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	table := map[string][]netip.Addr{
		"api.example.com":  {netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("2001:db8::5")},
		"evil.example.com": {netip.MustParseAddr("10.0.0.1")},
	}
	refuseDoH := false
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseDoH {
			http.Error(w, "DNS access denied", http.StatusForbidden)
			return
		}
		q, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(dnsStubAnswer(t, q, table))
	}))
	defer doh.Close()
	tcpDNS, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpDNS.Close()
	go func() {
		for {
			c, err := tcpDNS.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(l[:]))
				io.ReadFull(c, q)
				a := dnsStubAnswer(t, q, table)
				binary.BigEndian.PutUint16(l[:], uint16(len(a)))
				c.Write(append(l[:], a...))
			}()
		}
	}()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	// The "tailnet" dialer: every address the path dials is recorded, and
	// only the exit node's peer API, the dns= resolver and the one public
	// destination exist. Anything else fails the test.
	var mu sync.Mutex
	var dials []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dials = append(dials, addr)
		mu.Unlock()
		switch addr {
		case "100.64.0.9:41641":
			return net.Dial("tcp", doh.Listener.Addr().String())
		case "203.0.113.53:53":
			return net.Dial("tcp", tcpDNS.Addr().String())
		case "203.0.113.5:443":
			return net.Dial("tcp", echo.Addr().String())
		}
		t.Errorf("unexpected dial of %s", addr)
		return nil, fmt.Errorf("no route to %s", addr)
	}
	peer := &ipnstate.PeerStatus{ID: "nEXIT1CNTRL", DNSName: "exit-fra.corp.example.", HostName: "exit-fra",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}, Online: true, ExitNodeOption: true,
		PeerAPIURL: []string{"http://[fd7a:115c:a1e0::9]:41641", "http://100.64.0.9:41641"}}
	st := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{key.NewNode().Public(): peer}}
	status := func(context.Context) (*ipnstate.Status, error) { return st, nil }
	var edits []tailcfg.StableNodeID
	edit := func(_ context.Context, mp *ipn.MaskedPrefs) error {
		if !mp.ExitNodeIDSet {
			t.Error("EditPrefs without ExitNodeIDSet")
		}
		edits = append(edits, mp.Prefs.ExitNodeID)
		peer.ExitNode = true
		return nil
	}
	x := newExitNodeInternet(ExitNodeSpec{Node: "exit-fra"}, dial, status, edit, nil)
	stale := func() { x.mu.Lock(); x.checked = time.Time{}; x.mu.Unlock() }
	dialed := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(dials) }
	wantCode := func(err error, code, detail string) {
		t.Helper()
		e, ok := netd.AsError(err)
		if !ok || e.Code != code || !strings.Contains(e.Detail, detail) {
			t.Fatalf("err = %v, want %s containing %q", err, code, detail)
		}
	}

	// Resolve: selects the exit node (once), asks its peer API over the
	// tailnet dialer, returns every answer (the guard is netd's).
	addrs, err := x.Resolve(ctx, "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := table["api.example.com"]; !slices.Equal(addrs, want) {
		t.Fatalf("Resolve = %v, want %v", addrs, want)
	}
	if !slices.Equal(edits, []tailcfg.StableNodeID{"nEXIT1CNTRL"}) {
		t.Fatalf("EditPrefs = %v", edits)
	}
	if d := dialed(); len(d) == 0 || d[0] != "100.64.0.9:41641" {
		t.Fatalf("DNS went to %v, want the exit node's IPv4 peer API", d)
	}
	if addrs, _ = x.Resolve(ctx, "evil.example.com"); !slices.Equal(addrs, table["evil.example.com"]) {
		t.Fatalf("private answer must be returned for netd's guard to refuse: %v", addrs)
	}
	if p := x.Problem(); p != "" {
		t.Fatalf("Problem = %q", p)
	}
	ci := x.Internet()
	if ci.Mode != "exit-node=exit-fra" || ci.Via != "exit node exit-fra" || !ci.ExitNode() || ci.Problem() != "" {
		t.Fatalf("Internet() = %+v", ci)
	}
	h := &Host{cfg: Config{WorkerInternet: ci}}
	if !slices.Contains(h.features(), features.InternetExitNode) || slices.Contains((&Host{}).features(), features.InternetExitNode) {
		t.Fatalf("features: %v / %v", h.features(), (&Host{}).features())
	}

	// Dial: through the tailnet dialer, to the checked address.
	c, err := x.Dial(ctx, "tcp", "203.0.113.5:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	c.Close()
	before := len(dialed())

	// Offline: refused before any dial.
	peer.Online = false
	stale()
	_, err = x.Dial(ctx, "tcp", "203.0.113.5:443")
	wantCode(err, netd.CodeExitNodeUnavailable, "exit-fra.corp.example is offline")
	_, err = x.Resolve(ctx, "api.example.com")
	wantCode(err, netd.CodeExitNodeUnavailable, "is offline")
	if x.Problem() == "" {
		t.Fatal("Problem empty while offline")
	}
	peer.Online = true

	// Not offered (no exit routes, or no autogroup:internet grant).
	peer.ExitNodeOption = false
	stale()
	_, err = x.Dial(ctx, "tcp", "203.0.113.5:443")
	wantCode(err, netd.CodeExitNodeUnavailable, "autogroup:internet")
	peer.ExitNodeOption = true

	// Gone from the netmap.
	saved := st.Peer
	st.Peer = nil
	stale()
	_, err = x.Dial(ctx, "tcp", "203.0.113.5:443")
	wantCode(err, netd.CodeExitNodeUnavailable, "not in this host's netmap")
	st.Peer = saved
	if n := len(dialed()); n != before {
		t.Fatalf("%d dials while unavailable, want none", n-before)
	}

	// Deselected behind our back: selected again, from a fresh status.
	peer.ExitNode = false
	stale()
	if _, err := x.Dial(ctx, "tcp", "203.0.113.5:443"); err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 || !peer.ExitNode {
		t.Fatalf("reselect: edits %v, selected %v", edits, peer.ExitNode)
	}

	// The exit node refuses DNS for a tagged peer: names fail closed, with
	// the dns= hint; IP literals aren't affected (netd never resolves them).
	refuseDoH = true
	stale()
	_, err = x.Resolve(ctx, "api.example.com")
	wantCode(err, netd.CodeExitNodeUnavailable, "refuses DNS")
	if !strings.Contains(err.Error(), "dns=<resolver>") {
		t.Fatalf("no dns= hint: %v", err)
	}
	refuseDoH = false

	// dns=<ip>: plain DNS over TCP to that resolver, through the exit node.
	y := newExitNodeInternet(ExitNodeSpec{Node: "nEXIT1CNTRL", DNS: netip.AddrPortFrom(netip.MustParseAddr("203.0.113.53"), 53)}, dial, status, edit, nil)
	addrs, err = y.Resolve(ctx, "api.example.com")
	if err != nil || !slices.Equal(addrs, table["api.example.com"]) {
		t.Fatalf("tcp DNS: %v %v", addrs, err)
	}
	if d := dialed(); d[len(d)-1] != "203.0.113.53:53" {
		t.Fatalf("tcp DNS went to %s", d[len(d)-1])
	}
	if m := y.Internet().Mode; m != "exit-node=nEXIT1CNTRL,dns=203.0.113.53:53" {
		t.Fatalf("Mode = %q", m)
	}
	x.Close()
	y.Close()
	x.Close() // idempotent
}

func TestMatchExitNode(t *testing.T) {
	t.Parallel()
	p := &ipnstate.PeerStatus{ID: "nABC", DNSName: "Exit-FRA.corp.example.", HostName: "exit-fra-box", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}}
	for node, want := range map[string]bool{
		"nABC": true, "exit-fra": true, "EXIT-FRA.corp.example": true, "exit-fra-box": true, "100.64.0.9": true,
		"nabc": false, "exit": false, "fra": false, "": false, "100.64.0.10": false,
	} {
		if got := matchExitNode(p, node); got != want {
			t.Errorf("matchExitNode(%q) = %v, want %v", node, got, want)
		}
	}
	if matchExitNode(nil, "x") {
		t.Error("nil peer")
	}
}

func TestParseExitNodeSpec(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"exit-fra":                    "exit-fra",
		"exit-fra,dns=1.1.1.1":        "exit-fra,dns=1.1.1.1:53",
		"nABC, dns=[2606:4700::1111]": "nABC,dns=[2606:4700::1111]:53",
		"100.64.0.9,dns=9.9.9.9:5353": "100.64.0.9,dns=9.9.9.9:5353",
	} {
		sp, err := ParseExitNodeSpec(in)
		if err != nil || sp.String() != want {
			t.Errorf("ParseExitNodeSpec(%q) = %q, %v; want %q", in, sp.String(), err, want)
		}
	}
	for _, bad := range []string{"", ",dns=1.1.1.1", "x,dns=", "x,dns=one.one.one.one", "x,foo=1", "x,1.1.1.1"} {
		if _, err := ParseExitNodeSpec(bad); err == nil {
			t.Errorf("ParseExitNodeSpec(%q): no error", bad)
		}
	}
}
