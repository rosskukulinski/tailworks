package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func limitedReq(t *testing.T, h http.Handler, p Principal) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/workloads", nil)
	r.RemoteAddr = "100.64.0.7:41000"
	if p.Kind != "" {
		r = r.WithContext(WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func wantRateLimited(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", w.Code, w.Body)
	}
	var e struct{ Code string }
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Code != "rate_limited" || w.Header().Get("Tw-Error") != "rate_limited" {
		t.Fatalf("code %q / Tw-Error %q, want rate_limited", e.Code, w.Header().Get("Tw-Error"))
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
}

// TestAPIRateLimitPerCaller (#115): each WhoIs node has its own token
// bucket (burst twice the rate); a caller over it gets 429 rate_limited
// with Retry-After, while another caller, a host (its own larger bucket)
// and mgmt's own node are unaffected. Tokens come back with time.
func TestAPIRateLimitPerCaller(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	a := NewAPILimiter(APILimits{RPS: 5, HostRPS: 50})
	now := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return now }
	h := a.Wrap(ok)
	alice := Principal{Kind: "user", NodeID: "nALICE", Login: "alice@example.com"}
	for i := range 10 { // burst = 2 × 5
		if w := limitedReq(t, h, alice); w.Code != 204 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	wantRateLimited(t, limitedReq(t, h, alice))

	if w := limitedReq(t, h, Principal{Kind: "user", NodeID: "nBOB"}); w.Code != 204 {
		t.Fatalf("another caller: %d", w.Code)
	}
	host := Principal{Kind: "host", NodeID: "nHOST", HostID: "h1"}
	for i := range 100 {
		if w := limitedReq(t, h, host); w.Code != 204 {
			t.Fatalf("host request %d: %d %s", i, w.Code, w.Body)
		}
	}
	wantRateLimited(t, limitedReq(t, h, host))
	for i := range 50 {
		if w := limitedReq(t, h, Principal{Kind: "local", NodeID: "nMGMT"}); w.Code != 204 {
			t.Fatalf("operator request %d: %d", i, w.Code)
		}
	}
	if w := limitedReq(t, h, Principal{}); w.Code != 204 { // outside TailnetHandler
		t.Fatalf("unix socket: %d", w.Code)
	}

	now = now.Add(time.Second) // 5 tokens back
	for i := range 5 {
		if w := limitedReq(t, h, alice); w.Code != 204 {
			t.Fatalf("after refill, request %d: %d", i, w.Code)
		}
	}
	wantRateLimited(t, limitedReq(t, h, alice))

	off := NewAPILimiter(APILimits{RPS: -1, InFlight: -1}).Wrap(ok)
	for i := range 1000 {
		if w := limitedReq(t, off, alice); w.Code != 204 {
			t.Fatalf("limits off, request %d: %d", i, w.Code)
		}
	}
}

// TestAPIInFlightGate (#115): non-host callers together hold at most
// InFlight requests; the next is 429 rate_limited until one finishes.
// Hosts are outside the gate, so a user flood can't delay a heartbeat.
func TestAPIInFlightGate(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var started sync.WaitGroup
	h := NewAPILimiter(APILimits{InFlight: 2}).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hold") == "1" {
			started.Done()
			<-release
		}
		w.WriteHeader(204)
	}))
	hold := func(p Principal) chan int {
		done := make(chan int, 1)
		started.Add(1)
		go func() {
			r := httptest.NewRequest("GET", "/v1/queues/q/pull?hold=1", nil)
			r = r.WithContext(WithPrincipal(r.Context(), p))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			done <- w.Code
		}()
		return done
	}
	d1 := hold(Principal{Kind: "user", NodeID: "nA"})
	d2 := hold(Principal{Kind: "mcp", NodeID: "nB"})
	started.Wait()
	wantRateLimited(t, limitedReq(t, h, Principal{Kind: "user", NodeID: "nC"}))
	d3 := hold(Principal{Kind: "host", NodeID: "nH", HostID: "h1"}) // not gated
	started.Wait()
	close(release)
	for _, d := range []chan int{d1, d2, d3} {
		if c := <-d; c != 204 {
			t.Fatalf("held request: %d", c)
		}
	}
	if w := limitedReq(t, h, Principal{Kind: "user", NodeID: "nC"}); w.Code != 204 {
		t.Fatalf("after the gate drained: %d", w.Code)
	}
}

// TestAPIRateLimitBehindTailnetHandler (#115): wired as tw-mgmt wires it
// (TailnetHandler → limiter → API), the bucket is keyed by the WhoIs node,
// not the connection: a caller reconnecting from a new port is still
// limited, and mgmt's own node (the operator console) never is.
func TestAPIRateLimitBehindTailnetHandler(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	srv := &Server{M: e.m, Self: SelfInfo{NodeID: "nMGMT", DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		host, _, _ := net.SplitHostPort(addr)
		switch host {
		case "100.64.0.6":
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		case "100.64.0.9":
			return WhoIsInfo{NodeID: "nMGMT", Tags: []string{MgmtTag}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	h := srv.TailnetHandler(whois, NewAPILimiter(APILimits{RPS: 2}).Wrap(srv.Handler()))
	get := func(from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/status", nil)
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := range 4 {
		if w := get(fmt.Sprintf("100.64.0.6:%d", 1000+i)); w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	wantRateLimited(t, get("100.64.0.6:2000"))
	for i := range 20 {
		if w := get("100.64.0.9:1"); w.Code != 200 {
			t.Fatalf("operator request %d: %d %s", i, w.Code, w.Body)
		}
	}
}
