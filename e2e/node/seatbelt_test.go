//go:build nodetier

package node_test

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
)

// seatbeltNodeChecks: from inside the sandboxed Node app (macOS, issue
// #121), raw TCP that bypasses the egress proxy fails (the internet, a
// host-loopback listener), hostd's unix sockets fail, a signal to tw-hostd
// (this test process) fails and a write to /tmp fails; its own app port and
// signalling itself work. Egress through the proxy is checked by
// EgressAttributed, which runs under the same profile.
func (e *env) seatbeltNodeChecks(t *testing.T, ws hostd.WorkloadStatus, base string) map[string]string {
	t.Helper()
	get := func(path string) map[string]any {
		code, _, b := do(e.hc, "GET", base+path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, b)
		}
		return obj(b)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	lport := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	tmpFile := fmt.Sprintf("/tmp/tw-seatbelt-node-%d", os.Getpid())
	defer os.Remove(tmpFile)
	got := map[string]string{}
	for name, q := range map[string]string{
		"tcp_1.1.1.1:443":   "/net?host=1.1.1.1&port=443",
		"tcp_host_loopback": "/net?host=127.0.0.1&port=" + lport,
		"unix_hostd_push_a": "/net?path=" + url.QueryEscape(e.a.Host.PushSocket()),
		"signal_hostd":      "/signal?pid=" + strconv.Itoa(os.Getpid()),
		"write_tmp":         "/fs?op=write&path=" + url.QueryEscape(tmpFile),
	} {
		m := get(q)
		if m["ok"] != false {
			t.Fatalf("node app (Seatbelt) %s: allowed (%v)", name, m)
		}
		got[name] = fmt.Sprint(m["code"])
	}
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatalf("node app (Seatbelt) wrote %s", tmpFile)
	}
	if m := get("/net?host=127.0.0.1&port=" + strings.TrimPrefix(ws.Node.Addr, "127.0.0.1:")); m["ok"] != true {
		t.Fatalf("node app can't connect to its own port %s: %v", ws.Node.Addr, m)
	}
	if m := get("/signal?pid=" + strconv.Itoa(ws.Node.PID)); m["ok"] != true {
		t.Fatalf("node app can't signal itself: %v", m)
	}
	if ws.Egress != hostd.EgressEnforced {
		t.Fatalf("node app under the strict Seatbelt profile reports egress %q", ws.Egress)
	}
	t.Logf("seatbelt node app: %v", got)
	return got
}
