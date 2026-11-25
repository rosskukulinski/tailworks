package netd

import (
	"io"
	"net/http"
	"net/http/httputil"
	"sync"
)

// KeepBodyEOF makes the outbound request body of a Rewrite-style
// httputil.ReverseProxy answer io.EOF itself once it has returned io.EOF,
// instead of reading the inbound body again. Call it from Rewrite.
//
// Why (#138): for a Content-Length body, http.Transport's writeLoop reads
// the body once more after the declared length, to check for extra bytes.
// If the upstream answers as soon as it has the whole body, the proxy
// writes the response headers (FlushInterval -1, and any SSE response,
// flushes them at once), and
// http.Server then closes the inbound request body. When the writeLoop's
// extra read loses that race it gets http.ErrBodyReadAfterClose, and the
// Transport closes the upstream connection while the response body is
// still streaming. The proxy then aborts the downstream response
// mid-body: the caller sees "unexpected EOF" (workerd: "Network connection
// lost"). An EOF that has already been seen can't change, so answering it
// again from here is always correct.
func KeepBodyEOF(pr *httputil.ProxyRequest) {
	if pr.Out.Body == nil || pr.Out.Body == http.NoBody {
		return
	}
	pr.Out.Body = &eofBody{rc: pr.Out.Body}
}

type eofBody struct {
	mu  sync.Mutex
	rc  io.ReadCloser
	eof bool
}

func (b *eofBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.eof {
		return 0, io.EOF
	}
	n, err := b.rc.Read(p)
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

func (b *eofBody) Close() error { return b.rc.Close() }
