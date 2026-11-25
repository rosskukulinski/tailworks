package netd_test

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// lateReadBody stands in for a descheduled http.Transport writeLoop
// (#138): once the body has returned io.EOF, the next Read (the
// Transport's check for bytes past Content-Length) waits until the
// response headers have gone out before it reaches the inbound body.
type lateReadBody struct {
	io.ReadCloser
	eof     bool
	flushed <-chan struct{}
}

func (b *lateReadBody) Read(p []byte) (int, error) {
	if b.eof {
		select {
		case <-b.flushed:
			time.Sleep(20 * time.Millisecond) // let http.Server close the body
		case <-time.After(2 * time.Second):
		}
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

// TestUnixUpstreamLateBodyRead: an upstream that answers (SSE, like the
// MCP server) as soon as it has the whole request body must not lose its
// response body when the proxy's post-EOF body read comes late. Without
// KeepBodyEOF the read hits http.ErrBodyReadAfterClose, the Transport
// closes the upstream connection, and the client gets a cut body ("bare"
// shows that the injected race does cut it).
func TestUnixUpstreamLateBodyRead(t *testing.T) {
	t.Run("UnixUpstream", func(t *testing.T) {
		b, err := lateBodyRoundTrip(t, netd.UnixUpstream)
		if err != nil {
			t.Fatalf("response body cut after the headers: %v (read %q)", err, b)
		}
		if !strings.Contains(b, `data: {"ok":true}`) {
			t.Fatalf("body = %q", b)
		}
	})
	t.Run("bare", func(t *testing.T) {
		b, err := lateBodyRoundTrip(t, func(sock string) http.Handler {
			return &httputil.ReverseProxy{
				Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sock)
				}},
				FlushInterval: -1,
				ErrorLog:      log.New(io.Discard, "", 0),
				Rewrite:       func(pr *httputil.ProxyRequest) { pr.SetURL(&url.URL{Scheme: "http", Host: "up"}) },
			}
		})
		if err == nil {
			t.Logf("a proxy without KeepBodyEOF kept the body (%q): net/http no longer races here", b)
		}
	})
}

// lateBodyRoundTrip POSTs a small JSON body through proxy(upstream socket)
// to an SSE upstream, with the post-EOF body read delayed until the
// response headers have been flushed.
func lateBodyRoundTrip(t *testing.T, proxy func(sock string) http.Handler) (string, error) {
	dir, err := os.MkdirTemp("", "tw-up") // short: unix socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "up.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	flushed := make(chan struct{})
	up := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(flushed)
		time.Sleep(100 * time.Millisecond) // the tool call (a DO round trip)
		io.WriteString(w, "event: message\ndata: {\"ok\":true}\n\n")
	})}
	go up.Serve(ln)
	t.Cleanup(func() { up.Close() })

	p := proxy(sock)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = &lateReadBody{ReadCloser: r.Body, flushed: flushed}
		p.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	resp, err := http.Post(front.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}
