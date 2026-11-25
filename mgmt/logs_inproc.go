package mgmt

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

// handlerTransport serves requests through an in-process http.Handler: the
// push handler of a host agent embedded in this process (EmbeddedPushURL,
// single-process tw up). pushInProcess buffers a JSON push; workload logs
// stream (tw logs -f), so the body is a pipe the handler writes into as it
// goes, and the response returns as soon as the handler writes its header.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.h == nil {
		return nil, errors.New("embedded host has no push handler (not registered in this process)")
	}
	pr, pw := io.Pipe()
	rw := &pipeResponse{hdr: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer func() {
			rw.WriteHeader(http.StatusOK) // a handler that wrote nothing
			pw.Close()
		}()
		t.h.ServeHTTP(rw, req)
	}()
	select {
	case <-rw.ready:
	case <-req.Context().Done():
		pr.CloseWithError(req.Context().Err())
		return nil, req.Context().Err()
	}
	return &http.Response{
		Status: http.StatusText(rw.status), StatusCode: rw.status, Header: rw.hdr,
		Body: pr, Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, ContentLength: -1,
	}, nil
}

// pipeResponse is the handler's side: headers once, then the body into the
// pipe. Flush is a no-op (every Write already reaches the reader).
type pipeResponse struct {
	hdr    http.Header
	pw     *io.PipeWriter
	once   sync.Once
	status int
	ready  chan struct{}
}

func (r *pipeResponse) Header() http.Header { return r.hdr }

func (r *pipeResponse) WriteHeader(code int) {
	r.once.Do(func() {
		r.status = code
		close(r.ready)
	})
}

func (r *pipeResponse) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.pw.Write(b)
}

func (r *pipeResponse) Flush() {}
