//go:build e2evm && (linux || darwin)

package vm

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestE2EVMTCPEgress (ADR-052): connect() from a Worker, a facet-mode and a
// plain-DO object in a microVM reaches a tailnet TCP service through the
// guest gateway's egress handlers, with the identity each mode promises
// (the far side's WhoIs of the source); a refusal is typed for
// ctx.tailnet.connect().
func TestE2EVMTCPEgress(t *testing.T) {
	s := startVMSuite(t, nil)
	echo, err := nettest.NewNode(context.Background(), s.ctlURL, nettest.DummyKey, "ttest-echo", filepath.Join(s.e.Dir, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	ln, err := echo.Listen("tcp", ":5432")
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
				// A netstack conn reset before we got here has no remote
				// address: RemoteAddr returns nil (#411).
				ra := c.RemoteAddr()
				if ra == nil {
					return
				}
				who, err := echo.LC.WhoIs(context.Background(), ra.String())
				if err != nil || who == nil || who.Node == nil {
					io.WriteString(c, "whois failed\n")
					return
				}
				io.WriteString(c, "hello "+who.Node.Name+"\n")
				io.Copy(c, bufio.NewReader(c))
			}(c)
		}
	}()
	base := s.deploy(hostdtest.Fixture("compat"), "compat", nil)
	wl, _ := s.e.Host.Object(s.e.TenantID, "compat", "", "compat")
	get := func(path, via, host string) map[string]any {
		t.Helper()
		code, v := s.do("GET", base+path+"/connect?"+url.Values{"host": {host}, "port": {"5432"}, "via": {via}}.Encode(), nil)
		r, _ := v.(map[string]any)
		if code != 200 || r == nil {
			t.Fatalf("%s %s: %d %v", path, via, code, v)
		}
		return r
	}
	for _, c := range []struct{ path, via, class, name string }{
		{"", "stock", "", ""},
		{"", "tailnet", "", ""},
		{"/_tw/o/Plain/p1", "stock", "", ""},
		{"/_tw/o/Facet/f1", "stock", "Facet", "f1"},
		{"/_tw/o/Facet/f2", "tailnet", "Facet", "f2"},
	} {
		r := get(c.path, c.via, "ttest-echo") // creates a facet object on first use
		want := wl.Hostname
		if c.class != "" {
			want = s.e.WaitObject("compat", c.class, c.name, time.Minute).Hostname
		}
		read, _ := r["read"].(string)
		if r["ok"] != true || !strings.HasPrefix(read, "hello "+want+".") {
			t.Fatalf("%s %s: %v, want hello %s.", c.path, c.via, r, want)
		}
	}
	if r := get("/_tw/o/Facet/f3", "tailnet", "127.0.0.1"); r["ok"] != false || r["code"] != "forbidden_destination" {
		t.Fatalf("loopback: %v", r)
	}
}
