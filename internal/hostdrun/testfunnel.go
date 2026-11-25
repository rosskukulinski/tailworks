package hostdrun

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

// testPublicListen stands in for tsnet's ListenFunnel in hermetic process
// tests (tw-hostd -test-public-listen-dir, -local-dev only): testcontrol
// issues no certificates and there is no Funnel relay. Each public node
// (ADR-029) gets a plain-HTTP loopback listener, and its address is written
// to <dir>/<node hostname>.addr, so a test can drive netd's public listener
// directly (e2e/dataplane routing, #422). Every accepted conn is an
// *ipn.FunnelConn whose source is the client's own loopback address, so
// netd treats it exactly as a Funnel conn (public kind, per-IP limits).
// The file is removed when the listener closes.
func testPublicListen(dir string) func(srv *tsnet.Server) (net.Listener, error) {
	return func(srv *tsnet.Server) (net.Listener, error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, srv.Hostname+".addr")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(ln.Addr().String()), 0o600); err != nil {
			ln.Close()
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			ln.Close()
			return nil, err
		}
		return &testFunnelLn{Listener: ln, path: path}, nil
	}
}

type testFunnelLn struct {
	net.Listener
	path string
}

func (l *testFunnelLn) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	src, _ := netip.ParseAddrPort(c.RemoteAddr().String())
	return &ipn.FunnelConn{Conn: c, Src: src}, nil
}

func (l *testFunnelLn) Close() error {
	// Only remove the file if it still names this listener (a reopened
	// listener has written its own).
	if b, err := os.ReadFile(l.path); err == nil && string(b) == l.Listener.Addr().String() {
		os.Remove(l.path)
	}
	return l.Listener.Close()
}
