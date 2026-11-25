package gen_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// egressRedirect lets the fake egress sockets answer a redirect: a request
// for /__redirect?to=<url> gets a 302 to <url> (the platform's egress
// fetches with redirect: "manual", so the tenant's own fetch follows it).
func egressRedirect(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/__redirect" {
		return false
	}
	http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	return true
}

// TestEgressSink pins ledger claim C17 ("no egress path skips netd's
// ADR-019 guard: connect, cache, redirect, WebSocket, node:net") with the
// generated config and a real workerd. Tenant code in the Worker and in a
// facet object tries every way out it has (fetch, a followed redirect, a
// WebSocket upgrade and constructor, cloudflare:sockets connect, the Cache
// API, node:net, node:http), each aimed at a loopback sink the test
// listens on. The sink must never see a connection: what workerd carries
// goes to hostd's egress sockets (here the fake host; in production hostd
// hands it to netd, whose guard netd.TestChain/SelfSSRFRefused and
// PeerNotVisible pin), and the rest fails inside workerd.
func TestEgressSink(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var hits atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	sinkURL := "http://" + ln.Addr().String() + "/sink"

	in := start(t, hostdtest.Fixture("egresssink"), nil)
	type result struct {
		Name   string          `json:"name"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	run := func(t *testing.T, tw map[string]any, as string) {
		code, _, r := in.get("/?sink="+url.QueryEscape(sinkURL), tw)
		raw, _ := r["raw"].(string)
		var res []result
		if code != 200 || json.Unmarshal([]byte(raw), &res) != nil || len(res) == 0 {
			t.Fatalf("probe: %d %v", code, r)
		}
		by := map[string]result{}
		for _, x := range res {
			t.Logf("%-20s ok=%-5v %s%s", x.Name, x.OK, hostdtest.Trim(string(x.Result), 160), x.Error)
			by[x.Name] = x
		}
		// The control: a plain fetch and a followed redirect both reach the
		// platform's egress, attributed to the caller, for the sink's URL.
		for _, name := range []string{"fetch", "redirect"} {
			x := by[name]
			var s struct {
				Status int
				Text   string
			}
			json.Unmarshal(x.Result, &s)
			var echo struct{ As, URL string }
			json.Unmarshal([]byte(s.Text), &echo)
			if !x.OK || s.Status != 200 || echo.As != as || !strings.Contains(echo.URL, ln.Addr().String()) {
				t.Errorf("%s did not go through the platform egress as %s: %+v", name, as, x)
			}
		}
		time.Sleep(200 * time.Millisecond) // a late dial would still count
		if n := hits.Load(); n != 0 {
			t.Fatalf("C17: tenant code reached the loopback sink directly (%d connections), skipping the egress path", n)
		}
	}
	t.Run("Worker", func(t *testing.T) { run(t, wl, "workload") })
	t.Run("FacetObject", func(t *testing.T) { run(t, obj("Sink", "s1"), "Sink/s1") })
}
