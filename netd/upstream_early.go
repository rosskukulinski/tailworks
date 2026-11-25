package netd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"syscall"
)

// KeepEarlyAnswer makes tr return the upstream's response when the
// upstream answers a request before reading all of its body and then
// closes the connection, instead of the body write error that the close
// causes (#288). It wraps tr.DialContext, which must be set, and returns
// the RoundTripper to use in place of tr.
//
// Why: workerd answers without reading the body when a handler rejects a
// request early (a killed object's 410, a 401 or 413). It then sends
// `Connection: close` and closes. When the body is bigger than the
// socket buffers (about 16 KB), the Transport is still writing it, and the
// write fails with EPIPE (ENOTCONN on macOS), while workerd's response is
// already readable on the conn. persistConn.roundTrip waits for the write
// result and the response in one select. It returns the write error as
// soon as it gets one, even when the read loop has the response in hand,
// so ReverseProxy answered 503 upstream_unavailable "write: broken pipe"
// for up to 95 % of such requests (TestUnixUpstreamEarlyAnswer). A browser or curl shows the response.
//
// The fix: while a request is being written (from httptrace GotConn to
// WroteRequest), once at least one write of it has reached the conn, a
// write that fails because the peer closed is reported as a success, and
// so are the writes after it. The Transport then waits for the response,
// which is either workerd's answer or, if workerd went away without one,
// a read error (still a 503). Writes are dropped, not buffered, so the
// body is still streamed. Discarded body writes end when the Transport
// closes the conn, which it does after a Connection: close response or
// after a read error. Everything else is unchanged:
//   - A first write that fails (a pooled conn workerd had already closed,
//     #287) still fails, so net/http still sees nothingWrittenError and
//     retries the request when it can.
//   - Writes after the request (a WebSocket after 101) report their
//     errors as before.
//   - A conn that has dropped writes fails any later request's first
//     write, so a request that still gets it from the pool sees
//     nothingWrittenError too.
func KeepEarlyAnswer(tr *http.Transport) http.RoundTripper {
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &earlyAnswerConn{Conn: c}, nil
	}
	return earlyAnswerTransport{tr}
}

type earlyAnswerTransport struct{ tr *http.Transport }

func (t earlyAnswerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var c *earlyAnswerConn
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			c, _ = info.Conn.(*earlyAnswerConn)
			if c != nil {
				c.startRequest()
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			if c != nil {
				c.endRequest()
			}
		},
	}
	return t.tr.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
}

type earlyAnswerConn struct {
	net.Conn

	mu       sync.Mutex
	writing  bool  // between GotConn and WroteRequest
	wrote    bool  // a write of the current request reached the conn
	dropping bool  // the current request's writes are being dropped
	dropErr  error // the write error that started dropping; the conn is dead
	closed   bool  // the Transport closed the conn
}

func (c *earlyAnswerConn) startRequest() {
	c.mu.Lock()
	c.writing, c.wrote, c.dropping = true, false, false
	c.mu.Unlock()
}

func (c *earlyAnswerConn) endRequest() {
	c.mu.Lock()
	c.writing, c.dropping = false, false
	c.mu.Unlock()
}

func (c *earlyAnswerConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if err := c.dropErr; err != nil {
		drop := c.dropping && c.writing && !c.closed
		c.mu.Unlock()
		if drop {
			return len(p), nil
		}
		return 0, err
	}
	c.mu.Unlock()

	n, err := c.Conn.Write(p)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.wrote = true
		return n, nil
	}
	if c.writing && !c.closed && (c.wrote || n > 0) && peerClosed(err) {
		c.dropErr, c.dropping = err, true
		return len(p), nil
	}
	return n, err
}

func (c *earlyAnswerConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

// peerClosed reports whether a write failed because the other end closed
// the connection.
func peerClosed(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENOTCONN) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPROTOTYPE) // macOS, a send racing the peer's close
}
