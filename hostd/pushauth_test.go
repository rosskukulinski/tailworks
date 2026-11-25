package hostd

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd"
)

// fakeWhoIs answers WhoIs for every remote address with one identity
// (what the test's "peer" currently is), and for mgmt's own address with
// mgmt's identity. It stands in for the host node's LocalClient.
type fakeWhoIs struct {
	mu   sync.Mutex
	peer *apitype.WhoIsResponse // the pusher
	err  error
	mgmt *apitype.WhoIsResponse // what the -mgmt IP resolves to
}

const mgmtIP = "100.64.0.9"

func (f *fakeWhoIs) whois(_ context.Context, addr string) (*apitype.WhoIsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(addr, mgmtIP) {
		if f.mgmt == nil {
			return nil, errors.New("no such peer")
		}
		return f.mgmt, nil
	}
	return f.peer, f.err
}

func (f *fakeWhoIs) set(peer *apitype.WhoIsResponse, err error) {
	f.mu.Lock()
	f.peer, f.err = peer, err
	f.mu.Unlock()
}

func node(id string, tags ...string) *apitype.WhoIsResponse {
	return &apitype.WhoIsResponse{Node: &tailcfg.Node{StableID: tailcfg.StableNodeID(id), Name: id + ".ttest.ts.net.", Tags: tags}}
}

// newPushTestHost is a Host with no workloads: kills are no-ops and
// assignment pushes only poke syncNow.
func newPushTestHost(t *testing.T, mgmtBase string, whois WhoIsFunc, status StatusFunc) *Host {
	t.Helper()
	h := &Host{cfg: Config{DataDir: t.TempDir()}, log: slog.New(slog.DiscardHandler), objects: map[string]*object{}, syncNow: make(chan struct{}, 1)}
	h.pushAuth = newPushAuth(mgmtBase, whois, status, h.log)
	return h
}

func pushReq(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func wantRefused(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != 403 || resp.Header.Get(netd.HeaderError) != "push_unauthorized" {
		t.Fatalf("want 403 push_unauthorized, got %d %q", resp.StatusCode, resp.Header.Get(netd.HeaderError))
	}
}

// TestPushAuthTsnet: the tsnet push surface serves kills and assignment
// pushes to mgmt's node only (tag:tw-mgmt and the pinned stable node ID),
// and nothing else.
func TestPushAuthTsnet(t *testing.T) {
	t.Parallel()
	mgmt := node("nMGMT", MgmtTag)
	f := &fakeWhoIs{mgmt: mgmt}
	h := newPushTestHost(t, "http://"+mgmtIP, f.whois, nil)
	// Start pins mgmt's node from the -mgmt address (a tailnet IP).
	if err := h.pushAuth.pin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.pushAuth.Pinned() != "nMGMT" {
		t.Fatalf("pinned %q", h.pushAuth.Pinned())
	}
	srv := httptest.NewServer(h.tsnetPushAPI())
	defer srv.Close()

	// (a) mgmt's node: both pushes work.
	f.set(mgmt, nil)
	if resp := pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1","reason":"test"}`); resp.StatusCode != 200 {
		t.Fatalf("kill from mgmt: %d", resp.StatusCode)
	}
	if resp := pushReq(t, srv, "POST", "/v1/push/assignments", `{}`); resp.StatusCode != 200 {
		t.Fatalf("assignments from mgmt: %d", resp.StatusCode)
	}
	select {
	case <-h.syncNow:
	default:
		t.Fatal("assignment push did not trigger a sync")
	}
	// (d) the operator's routes are not on this surface, even for mgmt.
	for _, rt := range [][2]string{{"GET", "/v1/status"}, {"POST", "/v1/purge"}, {"POST", "/v1/vm/check?tenant=t&workload=w"}} {
		if resp := pushReq(t, srv, rt[0], rt[1], ""); resp.StatusCode != 404 && resp.StatusCode != 405 {
			t.Fatalf("%s %s on the tsnet surface: %d, want 404/405", rt[0], rt[1], resp.StatusCode)
		}
	}
	// (b) a tailnet peer without the tag.
	f.set(node("nTEST", "tag:ttest"), nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
	wantRefused(t, pushReq(t, srv, "GET", "/v1/status", ""))
	// An untagged user node.
	f.set(node("nUSER"), nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/assignments", `{}`))
	// (c) the right tag on another node than the pinned one.
	f.set(node("nOTHER", MgmtTag), nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
	if h.pushAuth.Pinned() != "nMGMT" {
		t.Fatalf("pin moved to %q", h.pushAuth.Pinned())
	}
	// (e) WhoIs fails, or answers without a node.
	f.set(nil, errors.New("whois: no match"))
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
	f.set(&apitype.WhoIsResponse{}, nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
	// mgmt still gets through after the refusals.
	f.set(mgmt, nil)
	if resp := pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`); resp.StatusCode != 200 {
		t.Fatalf("kill from mgmt after refusals: %d", resp.StatusCode)
	}
	// A WhoIs that fails for every address also refuses mgmt (no identity).
	h2 := newPushTestHost(t, "http://"+mgmtIP, func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return nil, errors.New("down")
	}, nil)
	srv2 := httptest.NewServer(h2.tsnetPushAPI())
	defer srv2.Close()
	wantRefused(t, pushReq(t, srv2, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
}

// TestPushAuthRepin: mgmt's node recreated (new stable ID at the same
// -mgmt address) is accepted once the address resolves to it; an unrelated
// tag:tw-mgmt node is still refused.
func TestPushAuthRepin(t *testing.T) {
	t.Parallel()
	old, renewed := node("nMGMT1", MgmtTag), node("nMGMT2", MgmtTag)
	f := &fakeWhoIs{mgmt: old}
	h := newPushTestHost(t, "http://"+mgmtIP+":80", f.whois, nil)
	if err := h.pushAuth.pin(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.tsnetPushAPI())
	defer srv.Close()
	f.set(renewed, nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`)) // address still names the old node
	f.mu.Lock()
	f.mgmt = renewed
	f.mu.Unlock()
	h.pushAuth.mu.Lock()
	h.pushAuth.lastLookup = h.pushAuth.lastLookup.Add(-pushRelookupEvery) // past the rate limit
	h.pushAuth.mu.Unlock()
	if resp := pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`); resp.StatusCode != 200 {
		t.Fatalf("recreated mgmt node: %d", resp.StatusCode)
	}
	if h.pushAuth.Pinned() != "nMGMT2" {
		t.Fatalf("pinned %q", h.pushAuth.Pinned())
	}
	f.set(old, nil)
	h.pushAuth.mu.Lock()
	h.pushAuth.lastLookup = h.pushAuth.lastLookup.Add(-pushRelookupEvery)
	h.pushAuth.mu.Unlock()
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
}

// TestPushAuthPinByDNSName: -mgmt http://tw-mgmt.<tailnet> is resolved
// through the netmap; a peer at that name without the tag does not pin,
// and a mgmt node that is not in the netmap yet is pinned by the first
// tagged pusher.
func TestPushAuthPinByDNSName(t *testing.T) {
	t.Parallel()
	tags := views.SliceOf([]string{MgmtTag})
	st := func(ps ...*ipnstate.PeerStatus) StatusFunc {
		peers := map[key.NodePublic]*ipnstate.PeerStatus{}
		for _, p := range ps {
			peers[key.NewNode().Public()] = p
		}
		return func(context.Context) (*ipnstate.Status, error) {
			return &ipnstate.Status{Peer: peers}, nil
		}
	}
	f := &fakeWhoIs{}
	h := newPushTestHost(t, "https://tw-mgmt.ttest.ts.net", f.whois,
		st(&ipnstate.PeerStatus{ID: "nHOST2", DNSName: "tw-host-h2.ttest.ts.net."},
			&ipnstate.PeerStatus{ID: "nMGMT", DNSName: "tw-mgmt.ttest.ts.net.", Tags: &tags}))
	if err := h.pushAuth.pin(context.Background()); err != nil || h.pushAuth.Pinned() != "nMGMT" {
		t.Fatalf("pin by DNS name: %v %q", err, h.pushAuth.Pinned())
	}
	// Short name (no tailnet suffix) matches the first label.
	h = newPushTestHost(t, "http://tw-mgmt", f.whois, st(&ipnstate.PeerStatus{ID: "nMGMT", DNSName: "tw-mgmt.ttest.ts.net.", Tags: &tags}))
	if err := h.pushAuth.pin(context.Background()); err != nil || h.pushAuth.Pinned() != "nMGMT" {
		t.Fatalf("pin by short name: %v %q", err, h.pushAuth.Pinned())
	}
	// The name resolves to an untagged node: not pinned.
	h = newPushTestHost(t, "http://tw-mgmt.ttest.ts.net", f.whois, st(&ipnstate.PeerStatus{ID: "nIMP", DNSName: "tw-mgmt.ttest.ts.net."}))
	if err := h.pushAuth.pin(context.Background()); err == nil || h.pushAuth.Pinned() != "" {
		t.Fatalf("untagged node at mgmt's name pinned: %v %q", err, h.pushAuth.Pinned())
	}
	// Not in the netmap: no pin at Start; the first tagged pusher pins,
	// an untagged one never does.
	h = newPushTestHost(t, "http://tw-mgmt.ttest.ts.net", f.whois, st())
	if err := h.pushAuth.pin(context.Background()); err == nil {
		t.Fatal("pinned with an empty netmap")
	}
	srv := httptest.NewServer(h.tsnetPushAPI())
	defer srv.Close()
	f.set(node("nTEST", "tag:ttest"), nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
	if h.pushAuth.Pinned() != "" {
		t.Fatal("untagged pusher pinned")
	}
	f.set(node("nMGMT", MgmtTag), nil)
	if resp := pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`); resp.StatusCode != 200 {
		t.Fatalf("first tagged pusher: %d", resp.StatusCode)
	}
	if h.pushAuth.Pinned() != "nMGMT" {
		t.Fatalf("pinned %q", h.pushAuth.Pinned())
	}
	f.set(node("nOTHER", MgmtTag), nil)
	wantRefused(t, pushReq(t, srv, "POST", "/v1/push/kill", `{"objectID":"o1"}`))
}

// TestPushListenerRequiresWhoIs: New refuses a tailnet push listener
// without an authenticator.
func TestPushListenerRequiresWhoIs(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mc, _ := mgmtapi.NewClient("http://127.0.0.1:1")
	_, err = New(Config{HostID: "h", Mgmt: mc, DataDir: t.TempDir(), WorkerdBin: "workerd", PushListener: ln})
	if err == nil || !strings.Contains(err.Error(), "WhoIs") {
		t.Fatalf("New with PushListener and no WhoIs: %v", err)
	}
}

// TestLocalPushAPIKeepsOperatorRoutes: the unix-socket surface still
// serves status, purge and the pushes without WhoIs.
func TestLocalPushAPIKeepsOperatorRoutes(t *testing.T) {
	t.Parallel()
	h := newPushTestHost(t, "", nil, nil)
	srv := httptest.NewServer(h.pushAPI())
	defer srv.Close()
	for _, rt := range [][2]string{{"GET", "/v1/status"}, {"POST", "/v1/purge"}, {"POST", "/v1/push/assignments"}} {
		if resp := pushReq(t, srv, rt[0], rt[1], "{}"); resp.StatusCode != 200 {
			t.Fatalf("%s %s on the local socket: %d", rt[0], rt[1], resp.StatusCode)
		}
	}
}
