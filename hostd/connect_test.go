package hostd_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// serveWhoisTCP serves n:port: greet with "hello <WhoIs node name>\n",
// then echo. It is the far side's getCaller for a raw TCP flow.
func serveWhoisTCP(t *testing.T, n *nettest.Node, port string) {
	ln, err := n.Listen("tcp", ":"+port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// A netstack conn reset before we got here has no remote
				// address: RemoteAddr returns nil (#411).
				ra := c.RemoteAddr()
				if ra == nil {
					return
				}
				who, err := whoisNode(context.Background(), n, ra.String())
				if err != nil {
					io.WriteString(c, "whois failed: "+err.Error()+"\n")
					return
				}
				io.WriteString(c, "hello "+who.Node.Name+"\n")
				io.Copy(c, bufio.NewReader(c))
			}(c)
		}
	}()
}

// TestTCPEgress (ADR-052): stock connect() and ctx.tailnet.connect()
// end to end through real workerd, hostd and netd to a tailnet TCP
// service, which verifies the source with WhoIs: the Worker and a plain-DO
// object leave as the workload node, a facet object as its own node. The
// same refusals as fetch(), a kill resets an open socket, and flows are
// audited.
func TestTCPEgress(t *testing.T) {
	t.Parallel()
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	serveWhoisTCP(t, s.echo, "5432")
	wl := e.WaitActive("compat", e.Deploy(hostdtest.Fixture("compat"), "compat", nil), 60*time.Second)
	s.waitPeer(wl.IP)

	// at is the path that reaches where on the workload node.
	at := func(where, name string) string {
		switch where {
		case "Facet", "Plain":
			return "/_tw/o/" + where + "/" + name
		}
		return ""
	}
	dial := func(t *testing.T, where, name, via, dst string) map[string]any {
		t.Helper()
		host, port, _ := net.SplitHostPort(dst)
		q := url.Values{"host": {host}, "port": {port}, "via": {via}}
		code, r := s.getJSON("http://" + wl.IP + at(where, name) + "/connect?" + q.Encode())
		if code != 200 {
			t.Fatalf("%s %s: %d %v", where, via, code, r)
		}
		return r
	}
	echo := "ttest-echo:5432"

	t.Run("Identity", func(t *testing.T) {
		for _, c := range []struct{ where, name, via string }{
			{"Worker", "", "stock"},
			{"Worker", "", "tailnet"},
			{"Plain", "p1", "stock"},
			{"Facet", "f1", "stock"},
			{"Facet", "f2", "tailnet"},
		} {
			r := dial(t, c.where, c.name, c.via, echo)
			want := wl.Hostname
			if c.where == "Facet" {
				want = e.WaitObject("compat", "Facet", c.name, 60*time.Second).Hostname
			}
			read, _ := r["read"].(string)
			if r["ok"] != true || !strings.HasPrefix(read, "hello "+want+".") || !strings.HasSuffix(read, "ping\n") {
				t.Fatalf("%s/%s: %v, want hello %s.", c.where, c.via, r, want)
			}
		}
	})

	t.Run("PlainTailnetConnectRefused", func(t *testing.T) {
		r := dial(t, "Plain", "p2", "tailnet", echo)
		if r["ok"] != false || r["code"] != "not_supported" {
			t.Fatal(r)
		}
	})

	t.Run("Refusals", func(t *testing.T) {
		f1 := e.WaitObject("compat", "Facet", "f1", 60*time.Second)
		for _, c := range []struct{ dst, code string }{
			{"127.0.0.1:5432", "forbidden_destination"},
			{"169.254.169.254:80", "forbidden_destination"},
			// A same-host sibling is reached in process (netd/samehost.go),
			// on its ingress port only: another port is refused like a
			// closed one.
			{net.JoinHostPort(f1.IP, "5432"), "dial_failed"},
			{"100.100.99.99:5432", "peer_not_visible"},
			{"no-such-node.ttest.ts.net:5432", "peer_not_visible"},
		} {
			r := dial(t, "Facet", "f3", "tailnet", c.dst)
			if r["ok"] != false || r["code"] != c.code {
				t.Fatalf("%s: %v, want %s", c.dst, r, c.code)
			}
			// Stock connect() is refused too (untyped: a lost connection).
			if r := dial(t, "Facet", "f3", "stock", c.dst); r["ok"] != false {
				t.Fatalf("stock %s: %v", c.dst, r)
			}
		}
	})

	t.Run("Audit", func(t *testing.T) {
		f1 := e.WaitObject("compat", "Facet", "f1", 60*time.Second)
		deadline := time.Now().Add(10 * time.Second)
		for {
			var allow, deny bool
			for _, rec := range e.Audit(f1.ID).Items {
				if rec.Kind == "egress" && rec.Dst == echo && strings.Contains(string(rec.Detail), "tcp:") {
					allow = true
				}
			}
			f3 := e.WaitObject("compat", "Facet", "f3", 60*time.Second)
			for _, rec := range e.Audit(f3.ID).Items {
				if rec.Kind == "deny" && rec.Dst == "127.0.0.1:5432" && rec.Code == "forbidden_destination" {
					deny = true
				}
			}
			if allow && deny {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("audit: egress %v, deny %v", allow, deny)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("KillResetsSocket", func(t *testing.T) {
		dial(t, "Facet", "k1", "stock", echo) // create k1 and its node
		o := e.WaitObject("compat", "Facet", "k1", 60*time.Second)
		type res struct{ r map[string]any }
		done := make(chan res, 1)
		go func() {
			_, r := s.getJSON("http://" + wl.IP + at("Facet", "k1") + "/hold?" + url.Values{"host": {"ttest-echo"}, "port": {"5432"}, "via": {"stock"}}.Encode())
			done <- res{r: r}
		}()
		// Wait for the socket to be open on the host.
		deadline := time.Now().Add(10 * time.Second)
		for e.Host.OpenSockets(e.TenantID, "compat", o.ID) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the socket never opened")
			}
			time.Sleep(20 * time.Millisecond)
		}
		select {
		case r := <-done:
			t.Fatalf("the hold ended before the kill: %v", r.r)
		default:
		}
		st := time.Now()
		if killed, err := e.FM.Kill(context.Background(), o.ID, true, "test kill"); err != nil || len(killed) != 1 {
			t.Fatal(killed, err)
		}
		select {
		case r := <-done:
			// The socket ended (reset, or the kill took the object's
			// request down with it) long before the hold's 30 s.
			t.Logf("socket ended %v after the kill: %v", time.Since(st), r.r)
		case <-time.After(10 * time.Second):
			t.Fatal("the socket survived the kill")
		}
		deadline = time.Now().Add(5 * time.Second)
		for e.Host.OpenSockets(e.TenantID, "compat", o.ID) != 0 {
			if time.Now().After(deadline) {
				t.Fatal("the host still tracks the killed object's socket")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// TestTCPEgressInternet: connect() to an internet name takes the internet
// path under tailnet.egress.internet, as fetch() does (ADR-049), with a
// hermetic stand-in: tcp.example.com and other.example.com resolve to a
// documentation address the dialer sends to a local TCP server.
func TestTCPEgressInternet(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.WriteString(c, "internet stand-in\n")
				io.Copy(c, c)
			}(c)
		}
	}()
	inet := &hostd.ContainerInternet{
		HostAddrs: []netip.Addr{},
		Resolve: func(_ context.Context, h string) ([]netip.Addr, error) {
			switch h {
			case "tcp.example.com", "other.example.com":
				return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
			}
			return nil, fmt.Errorf("no such host")
		},
		Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
			if a != "203.0.113.7:5432" && a != "203.0.113.7:25" {
				return nil, fmt.Errorf("unreachable")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
	}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.WorkerInternet = inet
	})
	e := s.e
	deploy := func(t *testing.T, workload, internet string) hostd.ObjectStatus {
		t.Helper()
		files, err := hostdtest.Build(hostdtest.Fixture("compat"), nil)
		if err != nil {
			t.Fatal(err)
		}
		egress := ""
		if internet != "" {
			egress = `, "egress": {"internet": ` + internet + `}`
		}
		files["tailworks.jsonc"] = []byte(`{"name": "` + workload + `", "tailnet": {"tags": ["tag:ttest"]` + egress + `}}`)
		wl := e.WaitActive(workload, e.DeployFiles(files, workload), 60*time.Second)
		s.waitPeer(wl.IP)
		return wl
	}
	dialPort := func(t *testing.T, wl hostd.ObjectStatus, host, port, via string) map[string]any {
		t.Helper()
		code, r := s.getJSON("http://" + wl.IP + "/_tw/o/Facet/i1/connect?" + url.Values{"host": {host}, "port": {port}, "via": {via}}.Encode())
		if code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		return r
	}
	dial := func(t *testing.T, wl hostd.ObjectStatus, host string) map[string]any {
		t.Helper()
		return dialPort(t, wl, host, "5432", "tailnet")
	}
	t.Run("OnByDefault", func(t *testing.T) {
		wl := deploy(t, "tcp-inet-on", "")
		if r := dial(t, wl, "tcp.example.com"); r["ok"] != true || r["read"] != "internet stand-in\nping\n" {
			t.Fatal(r)
		}
	})
	t.Run("OptOut", func(t *testing.T) {
		wl := deploy(t, "tcp-inet-off", "false")
		if r := dial(t, wl, "tcp.example.com"); r["ok"] != false || r["code"] != netd.CodeInternetEgressUnavailable {
			t.Fatal(r)
		}
	})
	t.Run("Allowlist", func(t *testing.T) {
		wl := deploy(t, "tcp-inet-list", `["tcp.example.com"]`)
		if r := dial(t, wl, "tcp.example.com"); r["ok"] != true {
			t.Fatal(r)
		}
		if r := dial(t, wl, "other.example.com"); r["ok"] != false || r["code"] != netd.CodeInternetEgressUnavailable {
			t.Fatal(r)
		}
	})
	// The default port policy (ADR-052 follow-up, issue #339): port 25 is
	// refused for ctx.tailnet.connect() (typed) and stock connect()
	// (opaque, audited), and allowed by an entry that names the port, which
	// then covers that port only.
	t.Run("Port25", func(t *testing.T) {
		wl := deploy(t, "tcp-inet-smtp", "")
		if r := dialPort(t, wl, "tcp.example.com", "25", "tailnet"); r["ok"] != false || r["code"] != netd.CodeInternetPortRefused {
			t.Fatal(r)
		}
		if r := dialPort(t, wl, "tcp.example.com", "25", "stock"); r["ok"] != false {
			t.Fatal(r)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			n := 0
			for _, rec := range e.Audit("").Items {
				if rec.Kind == "deny" && rec.Dst == "tcp.example.com:25" && rec.Code == netd.CodeInternetPortRefused {
					n++
				}
			}
			if n >= 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("want two deny records with %s for tcp.example.com:25, got %d", netd.CodeInternetPortRefused, n)
			}
			time.Sleep(100 * time.Millisecond)
		}
		wl = deploy(t, "tcp-inet-smtp-ok", `["tcp.example.com:25"]`)
		if r := dialPort(t, wl, "tcp.example.com", "25", "tailnet"); r["ok"] != true || r["read"] != "internet stand-in\nping\n" {
			t.Fatal(r)
		}
		if r := dialPort(t, wl, "tcp.example.com", "25", "stock"); r["ok"] != true {
			t.Fatal(r)
		}
		if r := dial(t, wl, "tcp.example.com"); r["ok"] != false || r["code"] != netd.CodeInternetEgressUnavailable {
			t.Fatalf("an entry that names :25 must not cover :5432: %v", r)
		}
	})
}

// TestTCPEgressPostgres: postgres.js from a Worker and a facet object to a
// real Postgres reached over the tailnet (ttest-pg:5432 forwards to
// TW_TEST_POSTGRES_DSN's server). make e2e-pg-socket starts one.
func TestTCPEgressPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TW_TEST_POSTGRES_DSN not set (make e2e-pg-socket)")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := u.User.Password(); u.User.Username() != "postgres" || p != "twtest" {
		t.Skip("the fixture's DATABASE_URL expects postgres:twtest")
	}
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	pg, err := nettest.NewNode(context.Background(), s.ctl.URL, nettest.DummyKey, "ttest-pg", filepath.Join(e.Dir, "pg"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	ln, err := pg.Listen("tcp", ":5432")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				up, err := net.Dial("tcp", u.Host)
				if err != nil {
					return
				}
				defer up.Close()
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	wl := e.WaitActive("pgprobe", e.Deploy(hostdtest.Fixture("pgprobe"), "pgprobe", nil), 60*time.Second)
	s.waitPeer(wl.IP)
	if err := s.client.WaitPeers(context.Background(), pg.IP4); err != nil {
		t.Fatal(err)
	}
	code, r := s.getJSON("http://" + wl.IP + "/")
	if code != 200 || r["two"] != float64(2) || r["user"] != "postgres" {
		t.Fatalf("Worker: %d %v", code, r)
	}
	// A facet object (its own node) writes and reads back.
	name := "pg-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var last float64
	for i := 1; i <= 2; i++ {
		code, r = s.getJSON("http://" + wl.IP + "/count/" + name)
		if code != 200 || r["n"] != float64(i) {
			t.Fatalf("facet: %d %v, want n=%d", code, r, i)
		}
		last = r["n"].(float64)
	}
	t.Logf("facet counter %s reached %v", name, last)
}
