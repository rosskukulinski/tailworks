// Package inproc is the transport between a host agent (hostd) and a
// tw-mgmt that run in the same process (single-process `tw up`): an
// http.RoundTripper that serves every request directly through mgmt's
// /v1 handler, with the request context carrying the host principal
// (mgmt.Principal{Kind: "host", HostID}) and the embedded-host marker
// (mgmt.WithEmbeddedHost). It bypasses the listeners' auth wrappers
// (TailnetHandler's WhoIs, LocalHandler's token) but nothing else: the
// role gate, requireLease and hostScope apply exactly as for a host on its
// own tailnet node.
//
// The marker is a context value, so it cannot arrive over any listener;
// mgmt accepts a registration with push URL "inproc://" only with it.
package inproc

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// RemoteAddr is the sentinel RemoteAddr of in-process requests (no host:port,
// so handlers that take a node address from it get none).
const RemoteAddr = "inproc"

// Transport serves requests through Handler as host HostID.
type Transport struct {
	Handler http.Handler
	HostID  string
}

// NewClient returns a mgmtapi.Client whose every request is served through
// h as host hostID, with no network in between. The client's Base is a
// placeholder ("http://mgmt"), like the unix-socket client's.
func NewClient(h http.Handler, hostID string) *mgmtapi.Client {
	return &mgmtapi.Client{Base: "http://mgmt", HostID: hostID,
		HTTP: &http.Client{Transport: &Transport{Handler: h, HostID: hostID}, Timeout: 60 * time.Second}}
}

// RoundTrip runs the handler in a goroutine and streams its response: the
// headers are returned as soon as the handler writes them (or finishes),
// the body is a pipe. Cancelling the request's context cancels the
// handler's (it sees r.Context().Done()) and fails the round trip.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Handler == nil {
		return nil, errors.New("inproc: no handler")
	}
	ctx := mgmt.WithPrincipal(req.Context(), mgmt.Principal{Kind: "host", HostID: t.HostID})
	ctx = mgmt.WithEmbeddedHost(ctx, t.HostID)
	in := req.Clone(ctx)
	in.RemoteAddr = RemoteAddr
	in.RequestURI = req.URL.RequestURI()
	if in.Body == nil {
		in.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	rw := &responseWriter{hdr: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				rw.abort(fmt.Errorf("inproc: handler panic: %v", p))
			}
			rw.finish()
		}()
		t.Handler.ServeHTTP(rw, in)
	}()
	select {
	case <-rw.ready:
	case <-ctx.Done():
		pr.CloseWithError(ctx.Err())
		return nil, ctx.Err()
	}
	if rw.err != nil {
		pr.CloseWithError(rw.err)
		return nil, rw.err
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", rw.status, http.StatusText(rw.status)),
		StatusCode:    rw.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rw.sent,
		Body:          pr,
		ContentLength: -1,
		Request:       req,
	}, nil
}

// responseWriter is the handler's side: headers are snapshotted when the
// status is written (closing ready), the body goes down the pipe.
type responseWriter struct {
	hdr   http.Header
	pw    *io.PipeWriter
	ready chan struct{}
	once  sync.Once

	// Written by the handler goroutine before ready is closed; read by
	// RoundTrip after.
	status int
	sent   http.Header
	err    error
}

func (w *responseWriter) Header() http.Header { return w.hdr }

func (w *responseWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		w.sent = w.hdr.Clone()
		close(w.ready)
	})
}

func (w *responseWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(b)
}

// Flush sends the headers if they have not been sent (streaming handlers).
func (w *responseWriter) Flush() { w.WriteHeader(http.StatusOK) }

// abort fails the round trip (before headers) or the body (after).
func (w *responseWriter) abort(err error) {
	w.once.Do(func() {
		w.err = err
		close(w.ready)
	})
	w.pw.CloseWithError(err)
}

func (w *responseWriter) finish() {
	w.WriteHeader(http.StatusOK)
	w.pw.Close()
}

// Compile-time: the writer flushes (hostd and mgmt stream some responses).
var _ http.Flusher = (*responseWriter)(nil)
