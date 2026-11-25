package inproc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// newMgmt is a real mgmt (SQLite store, file custody) with its /v1 API.
func newMgmt(t *testing.T) (*mgmt.Server, *mgmt.Mgmt) {
	t.Helper()
	dir := t.TempDir()
	st, err := mgmt.OpenStore(context.Background(), filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, IDSecret: bytes.Repeat([]byte{9}, 32), ReapInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return &mgmt.Server{M: m, Self: mgmt.SelfInfo{Tailnet: "t.ts.net"}}, m
}

// The in-process client is the host principal with its host ID: whoami
// says so, host routes work, admin routes are refused, and a registration
// with push URL inproc:// is accepted (201) and recorded without a node.
func TestClientIsHostPrincipal(t *testing.T) {
	api, _ := newMgmt(t)
	c := NewClient(api.Handler(), "h1")
	ctx := context.Background()
	var who mgmt.WhoAmI
	if err := c.Do(ctx, "GET", "/v1/whoami", nil, &who); err != nil {
		t.Fatal(err)
	}
	if who.Kind != "host" || who.HostID != "h1" || who.Admin || who.Actor != "host:h1" {
		t.Fatalf("whoami through the in-process transport: %+v", who)
	}
	// Admin routes: refused like for any host (the role gate).
	err := c.Do(ctx, "GET", "/v1/status", nil, nil)
	if e, ok := mgmtapi.AsError(err); !ok || e.Status != 403 || e.Code != "forbidden" {
		t.Fatalf("GET /v1/status as the embedded host: %v", err)
	}
	err = c.Do(ctx, "POST", "/v1/tenants", map[string]any{"tenantID": "tn_x", "tailnet": "x.ts.net"}, nil)
	if e, ok := mgmtapi.AsError(err); !ok || e.Status != 403 || e.Code != "forbidden" {
		t.Fatalf("POST /v1/tenants as the embedded host: %v", err)
	}
	// Another host's routes: refused (the host ID is the transport's).
	err = c.Do(ctx, "GET", "/v1/hosts/h2/assignments", nil, nil)
	if e, ok := mgmtapi.AsError(err); !ok || e.Status != 403 {
		t.Fatalf("GET /v1/hosts/h2/assignments as h1: %v", err)
	}
	// Registration as the embedded host: no node, no secret, inproc:// push.
	if err := c.Register(ctx, mgmtapi.RegisterRequest{HostID: "h1", CapacityNodes: 10, Tiers: []string{"process"}, PushURL: mgmt.EmbeddedPushURL}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var h struct {
		HostID, Status, NodeID string
		Push                   *struct {
			Port int
			URL  string
		}
	}
	if err := c.Do(ctx, "GET", "/v1/hosts/h1", nil, &h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "live" || h.NodeID != "" || h.Push == nil || h.Push.Port != 0 {
		t.Fatalf("embedded host view: %+v push %+v", h, h.Push)
	}
	// Heartbeats and assignments work as the host.
	if _, err := c.Heartbeat(ctx, "h1", mgmtapi.HeartbeatRequest{Leases: []mgmtapi.Lease{}}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := c.Assignments(ctx, "h1"); err != nil {
		t.Fatalf("assignments: %v", err)
	}
	// Registering again (a restart) is fine: 200, still no node.
	if err := c.Register(ctx, mgmtapi.RegisterRequest{HostID: "h1", CapacityNodes: 10, Tiers: []string{"process"}, PushURL: mgmt.EmbeddedPushURL}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
}

// inproc:// is reserved for the in-process transport: a tag:tw-host node
// over the tailnet (TailnetHandler) and a local-dev caller (LocalHandler)
// naming it are refused, with nothing recorded.
func TestEmbeddedPushURLRefusedOnListeners(t *testing.T) {
	api, _ := newMgmt(t)
	body := func() io.Reader {
		return strings.NewReader(`{"capacityNodes":1,"tiers":["process"],"push":{"url":"inproc://"}}`)
	}
	// A tailnet host node: first registration of an unknown host.
	whois := func(ctx context.Context, addr string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nHOST", Name: "tw-host-h9.t.ts.net.", Tags: []string{mgmt.HostTag}}, nil
	}
	ts := httptest.NewServer(api.TailnetHandler(whois, api.Handler()))
	defer ts.Close()
	req, _ := http.NewRequest("PUT", ts.URL+"/v1/hosts/h9", body())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "host_identity_mismatch") || !strings.Contains(string(b), "inproc://") {
		t.Fatalf("tailnet host naming inproc://: %d %s", resp.StatusCode, b)
	}
	// The local-dev socket (token): the local principal may create hosts,
	// but not embedded ones.
	ls := httptest.NewServer(mgmt.LocalHandler(api.Handler(), "tok"))
	defer ls.Close()
	req, _ = http.NewRequest("PUT", ls.URL+"/v1/hosts/h9", body())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mgmt.HostTokenHeader, "tok")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "host_identity_mismatch") {
		t.Fatalf("local-dev caller naming inproc://: %d %s", resp.StatusCode, b)
	}
	// Nothing was recorded.
	c := NewClient(api.Handler(), "h9")
	err = c.Do(context.Background(), "GET", "/v1/hosts/h9", nil, nil)
	if e, ok := mgmtapi.AsError(err); !ok || e.Status != 404 {
		t.Fatalf("h9 after the refusals: %v", err)
	}
}

// The transport streams: headers arrive when written (before the body is
// complete), request bodies round-trip, and cancelling the request's
// context ends a handler that waits on it (long polls).
func TestTransportStreamingAndCancel(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 3<<20)
	gate := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Len", "n")
			w.WriteHeader(201)
			w.Write(b)
		case "/stream":
			w.WriteHeader(200)
			w.Write([]byte("first"))
			w.(http.Flusher).Flush()
			<-gate
			w.Write([]byte("second"))
		case "/wait":
			<-r.Context().Done()
			w.WriteHeader(504)
		case "/principal":
			if r.RemoteAddr != RemoteAddr {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]bool{"embedded": mgmt.IsEmbeddedHost(r.Context())})
		}
	})
	hc := &http.Client{Transport: &Transport{Handler: h, HostID: "h1"}}
	resp, err := hc.Post("http://mgmt/echo", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || resp.Header.Get("X-Len") != "n" || !bytes.Equal(got, big) {
		t.Fatalf("echo: %d %s %d bytes", resp.StatusCode, resp.Header.Get("X-Len"), len(got))
	}
	resp, err = hc.Get("http://mgmt/stream")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "first" {
		t.Fatalf("stream first chunk: %q %v", buf, err)
	}
	close(gate)
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(rest) != "second" {
		t.Fatalf("stream rest: %q", rest)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://mgmt/wait", nil)
	st := time.Now()
	if _, err := hc.Do(req); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("wait: %v after %v", err, time.Since(st))
	}
	resp, err = hc.Get("http://mgmt/principal")
	if err != nil {
		t.Fatal(err)
	}
	var pr struct{ Embedded bool }
	json.NewDecoder(resp.Body).Decode(&pr)
	resp.Body.Close()
	if !pr.Embedded {
		t.Fatal("the request context does not carry the embedded-host marker")
	}
}
