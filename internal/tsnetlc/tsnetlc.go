// Package tsnetlc gives a tsnet.Server's LocalAPI client a transport that
// Close can drain first, so closing the node never waits out the LocalAPI
// server's 5 s shutdown grace for a connection nobody uses (#245).
//
// tsnet.Server.Close shuts down its LocalAPI http.Server with a 5 s
// deadline. http.Server.Shutdown treats a connection that has not sent its
// first request yet (StateNew) as active for 5 s. local.Client's default
// http.Transport leaves exactly such connections behind: when a request
// waits for a connection and an idle one frees up before its own dial
// completes, the dialed connection goes to the idle pool without a request
// on it. Seen on real hosts: tw-hostd's node took 5.0 s to close on about
// half of `systemctl --user restart tw`, the goroutine dump showing the
// LocalAPI server's Shutdown and one server conn in readRequest over a
// memnet pipe whose client side sat idle in the transport.
package tsnetlc

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

var transports sync.Map // *tsnet.Server → *http.Transport

// dialHook, if set, runs each LocalAPI dial in place of calling dial
// directly (tests: holding a dial until its request has given up leaves the
// stray connection behind).
var dialHook atomic.Pointer[func(dial func() (net.Conn, error)) (net.Conn, error)]

// Client starts ts and returns its LocalAPI client, with a transport Close
// drains. Call it before anything uses ts.LocalClient (tsnet hands out one
// shared *local.Client, which picks its transport on first use), on the
// goroutine that set ts up. Nothing else about the client changes.
func Client(ts *tsnet.Server) (*local.Client, error) {
	lc, err := ts.LocalClient()
	if err != nil {
		return nil, err
	}
	if _, ok := transports.Load(ts); !ok {
		dial := lc.Dial
		tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := func() (net.Conn, error) { return dial(ctx, network, addr) }
			if h := dialHook.Load(); h != nil {
				return (*h)(d)
			}
			return d()
		}}
		if _, loaded := transports.LoadOrStore(ts, tr); !loaded {
			lc.Transport = tr
		}
	}
	return lc, nil
}

// Close closes the LocalAPI client's idle connections, then ts.
func Close(ts *tsnet.Server) error {
	if v, ok := transports.LoadAndDelete(ts); ok {
		v.(*http.Transport).CloseIdleConnections()
	}
	return ts.Close()
}
