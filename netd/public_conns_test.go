package netd_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/publicspec"
	"github.com/rosskukulinski/tailworks/netd"
)

// dialAs opens a raw connection to the public listener that netd sees as
// coming from src.
func (e *pubEnv) dialAs(t *testing.T, src netip.AddrPort) net.Conn {
	t.Helper()
	e.mapped.Store(true)
	e.mu.Lock()
	addr := e.addr
	e.mu.Unlock()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	e.srcs.Store(c.LocalAddr().String(), src)
	t.Cleanup(func() { c.Close() })
	return c
}

// getOn sends one keep-alive GET on c and reports whether it was served
// (200). A refused connection (RST at accept) reports false.
func getOn(c net.Conn, br *bufio.Reader, path string) bool {
	c.SetDeadline(time.Now().Add(3 * time.Second))
	defer c.SetDeadline(time.Time{})
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", path); err != nil {
		return false
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == 200
}

func waitSummary(t *testing.T, e *pubEnv) netd.PublicRejectSummary {
	t.Helper()
	select {
	case s := <-e.rejs:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no reject summary")
	}
	return netd.PublicRejectSummary{}
}

// TestRT3_H09_MaxConnsOneIP (security/RED-TEAM.md RT3-09): one address that
// opens MaxConns keep-alive connections no longer closes the workload's
// public ingress for everyone else; it holds only MaxConnsPerIP of them.
func TestRT3_H09_MaxConnsOneIP(t *testing.T) {
	restore := netd.SetPublicRejectWindow(300 * time.Millisecond)
	defer restore()
	const maxConns, perIP = 8, 3
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/*"}, Limits: limits(func(l *publicspec.Limits) {
		l.MaxConns, l.MaxConnsPerIP = maxConns, perIP
	}), Enabled: true}, nil)
	attacker := netip.MustParseAddrPort("203.0.113.66:1000")

	// The attacker tries to take every slot and keep it.
	var held []net.Conn
	served := 0
	for i := range maxConns {
		src := netip.AddrPortFrom(attacker.Addr(), attacker.Port()+uint16(i))
		c := e.dialAs(t, src)
		if getOn(c, bufio.NewReader(c), "/a") {
			served++
			held = append(held, c)
		}
	}
	if served != perIP {
		t.Fatalf("one address holds %d connections, want %d (maxConnsPerIp)", served, perIP)
	}

	// Everyone else still gets through, up to MaxConns in total.
	for i := range maxConns - perIP {
		src := netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 51, 100, byte(10 + i)}), 443)
		c := e.dialAs(t, src)
		if !getOn(c, bufio.NewReader(c), "/b") {
			t.Fatalf("client %v refused while %v holds its cap", src, attacker.Addr())
		}
	}
	// MaxConns still bounds the total.
	if c := e.dialAs(t, netip.MustParseAddrPort("192.0.2.1:443")); getOn(c, bufio.NewReader(c), "/c") {
		t.Fatal("served over maxConns")
	}

	s := waitSummary(t, e)
	if s.IPConnsRefused != maxConns-perIP || s.ConnsRefused != 1 {
		t.Errorf("summary: ipConnsRefused %d (want %d), connsRefused %d (want 1)", s.IPConnsRefused, maxConns-perIP, s.ConnsRefused)
	}
	if len(s.TopIPs) == 0 || s.TopIPs[0].IP != attacker.Addr() || s.TopIPs[0].Count != maxConns-perIP {
		t.Errorf("top IPs %+v", s.TopIPs)
	}

	// Closing one of the attacker's connections frees its slot (for it, as
	// the total is now below maxConns).
	held[0].Close()
	time.Sleep(100 * time.Millisecond)
	if c := e.dialAs(t, attacker); !getOn(c, bufio.NewReader(c), "/d") {
		t.Error("the address's slot was not released")
	}
}

// IPv6 clients are capped per /64: rotating addresses inside one prefix
// doesn't get around the cap.
func TestPublicMaxConnsPerIPv6Prefix(t *testing.T) {
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/*"}, Limits: limits(func(l *publicspec.Limits) {
		l.MaxConnsPerIP = 1
	}), Enabled: true}, nil)
	a := e.dialAs(t, netip.MustParseAddrPort("[2001:db8:0:1::1]:1"))
	if !getOn(a, bufio.NewReader(a), "/a") {
		t.Fatal("first v6 conn refused")
	}
	if c := e.dialAs(t, netip.MustParseAddrPort("[2001:db8:0:1::2]:1")); getOn(c, bufio.NewReader(c), "/b") {
		t.Error("a second address in the same /64 was served over the cap")
	}
	if c := e.dialAs(t, netip.MustParseAddrPort("[2001:db8:0:2::1]:1")); !getOn(c, bufio.NewReader(c), "/c") {
		t.Error("another /64 was refused")
	}
}

// A new connection must send its first request within the first-request
// timeout; once it has, neither a slow body nor keep-alive idling is held
// to that deadline.
func TestPublicFirstRequestTimeout(t *testing.T) {
	restore := netd.SetPublicFirstRequestTimeout(300 * time.Millisecond)
	defer restore()
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/*"}, Limits: limits(nil), Enabled: true}, nil)
	src := netip.MustParseAddrPort("203.0.113.9:1")

	// Idle before the first request: closed.
	idle := e.dialAs(t, src)
	t0 := time.Now()
	idle.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := idle.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle conn got data")
	} else if el := time.Since(t0); el > 3*time.Second {
		t.Fatalf("idle conn closed after %v: %v", el, err)
	}

	// A body that arrives after the first-request deadline is still read.
	c := e.dialAs(t, src)
	br := bufio.NewReader(c)
	fmt.Fprintf(c, "POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\n")
	time.Sleep(600 * time.Millisecond)
	fmt.Fprintf(c, "hello")
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("slow body: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("slow body: %d", resp.StatusCode)
	}
	// And the connection stays usable after idling past the deadline.
	time.Sleep(600 * time.Millisecond)
	if !getOn(c, br, "/again") {
		t.Error("keep-alive conn closed by the first-request deadline")
	}
}

// An upgraded connection is closed after UpgradeLifetime, and its slot is
// released, even when the upstream never closes it.
func TestPublicUpgradeLifetime(t *testing.T) {
	hijacked := make(chan struct{}, 1)
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/ws/*"}, Upgrade: []string{"/ws/*"}, Limits: limits(func(l *publicspec.Limits) {
		l.UpgradeLifetimeSeconds, l.MaxConnsPerIP = 1, 1
	}), Enabled: true}, func(e *pubEnv) {
		e.up = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") == "" {
				io.WriteString(w, "ok")
				return
			}
			c, brw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer c.Close()
			brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			brw.Flush()
			hijacked <- struct{}{}
			io.Copy(io.Discard, c) // never closes on its own
		})
	})
	src := netip.MustParseAddrPort("203.0.113.10:1")
	c := e.dialAs(t, src)
	br := bufio.NewReader(c)
	fmt.Fprintf(c, "GET /ws/x HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	<-hijacked
	t0 := time.Now()
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("upgraded conn got data")
	}
	if el := time.Since(t0); el < 500*time.Millisecond || el > 4*time.Second {
		t.Fatalf("upgraded conn closed after %v, want about 1s", el)
	}
	time.Sleep(100 * time.Millisecond)
	if c2 := e.dialAs(t, src); !getOn(c2, bufio.NewReader(c2), "/ws/y") {
		t.Error("the upgraded conn's slot was not released")
	}
}
