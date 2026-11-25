package mgmt

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/rosskukulinski/tailworks/mgmt/ui"
)

// The admin UI (mgmt/ui) is an ordinary /v1 client: it takes an
// *http.Client and a base URL and uses only the public API. Embedded in
// mgmt, its client is uiTransport, which serves each request with the API
// handler directly instead of over a socket. The request's context is the
// browser request's, so the call carries the browser's authenticated
// principal and the role gate treats it like any other caller's. A UI run
// as its own process would get a network transport instead.

// uiBaseURL is the in-process API's nominal origin. Nothing dials it.
const uiBaseURL = "http://tw-mgmt.internal"

// embeddedUI returns the admin UI wired to api in-process.
func embeddedUI(api http.Handler) http.Handler {
	return ui.Handler(&http.Client{Transport: uiTransport{api}}, uiBaseURL)
}

type uiTransport struct{ api http.Handler }

func (t uiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request: serve a server-side copy.
	sr := req.Clone(req.Context())
	sr.RequestURI = req.URL.RequestURI()
	sr.Host = req.URL.Host
	if sr.Body == nil {
		sr.Body = http.NoBody
	}
	rec := &uiRecorder{hdr: http.Header{}}
	t.api.ServeHTTP(rec, sr)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &http.Response{
		Status:        strconv.Itoa(rec.status) + " " + http.StatusText(rec.status),
		StatusCode:    rec.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rec.hdr,
		Body:          io.NopCloser(&rec.body),
		ContentLength: int64(rec.body.Len()),
		Request:       req,
	}, nil
}

// uiRecorder buffers one in-process API response.
type uiRecorder struct {
	hdr    http.Header
	status int
	body   bytes.Buffer
}

func (r *uiRecorder) Header() http.Header { return r.hdr }

func (r *uiRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *uiRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}
