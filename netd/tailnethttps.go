package netd

// Tailnet HTTPS (issue #360, ADR-tailnet-https): an object node can also
// serve its attributed ingress on :443 with the node's own ts.net
// certificate, so code that insists on an https:// origin (OAuth resource
// URLs, secure cookies) can run on the tailnet. Plain :80 stays exactly as
// it was: the platform's own callers (ctx.tailnet.fetch, service bindings,
// same-host hops, Tailscale Services) keep using it.
//
//   - Only tailnet traffic: the listener is tsnet's Listen, never Funnel.
//   - The handler is serveIngress, the same as :80 (WhoIs, certificate
//     narrowing, x-tw-* stripping). A request that arrived over TLS gets
//     X-Forwarded-Proto: https, set after every client copy was stripped,
//     so the upstream can trust it (hostd turns it into an https://
//     request.url).
//   - The certificate comes from the node's LocalAPI (lc.GetCertificate).
//     A tailnet without HTTPS certificates (or without MagicDNS) has no
//     CertDomains: the listener is refused with
//     tailnet_https_unavailable instead of opening a port whose every
//     handshake would fail.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

// CodeTailnetHTTPSUnavailable: tailnet.https was asked for, but the
// tailnet issues no HTTPS certificates to this node.
const CodeTailnetHTTPSUnavailable = "tailnet_https_unavailable"

// TailnetHTTPSPort is the port a node with tailnet HTTPS serves TLS on.
const TailnetHTTPSPort = 443

// HTTPSCertFunc returns the certificate source for a node's :443
// listener, or an error (a *Error with CodeTailnetHTTPSUnavailable when
// the tailnet issues no certificates). dnsName is the node's MagicDNS
// name, without the trailing dot.
type HTTPSCertFunc func(ctx context.Context, lc *local.Client, dnsName string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error)

// errNoTailnetCerts is the refusal when the tailnet has no HTTPS
// certificates for the node.
func errNoTailnetCerts(why string) *Error {
	e := newErr(CodeTailnetHTTPSUnavailable, why+": turn on HTTPS certificates for the tailnet (admin console, DNS page; MagicDNS must be on too), or remove tailnet.https")
	e.status = http.StatusPreconditionFailed
	return e
}

// tailnetCerts is the default HTTPSCertFunc: the node's own certificate
// from its LocalAPI, when the tailnet issues one.
func tailnetCerts(ctx context.Context, lc *local.Client, _ string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	if lc == nil {
		return nil, errors.New("node has no local client")
	}
	st, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkTailnetCerts(st); err != nil {
		return nil, err
	}
	return lc.GetCertificate, nil
}

// checkTailnetCerts refuses a node whose status shows no certificate
// domains (tsnet's ListenTLS makes the same two checks).
func checkTailnetCerts(st *ipnstate.Status) error {
	if st.CurrentTailnet != nil && !st.CurrentTailnet.MagicDNSEnabled {
		return errNoTailnetCerts("MagicDNS is off on this tailnet")
	}
	if len(st.CertDomains) == 0 {
		return errNoTailnetCerts("this tailnet issues no HTTPS certificates")
	}
	return nil
}

// httpsState is an object's tailnet HTTPS listener.
type httpsState struct {
	mu   sync.Mutex
	want bool
	ln   net.Listener
	hs   *http.Server
	// stopPrefetch cancels the certificate prefetch (teardown).
	stopPrefetch context.CancelFunc
	// opening serializes openHTTPS (SetTailnetHTTPS and start can race).
	opening sync.Mutex
}

// SetTailnetHTTPS turns id's :443 listener on or off. On a ready node it
// opens (or closes) it now and returns the outcome: a
// CodeTailnetHTTPSUnavailable *Error when the tailnet issues no
// certificates (the setting then stays off). On a pending node it is
// recorded and opened when the node becomes ready.
func (r *Registry) SetTailnetHTTPS(id string, on bool) error {
	o := r.Get(id)
	if o == nil {
		if !on {
			return nil
		}
		return ErrNotFound
	}
	if o.pub != nil {
		if !on {
			return nil
		}
		return newErr(CodeBadRequest, "object "+id+" is a public node: it serves Funnel only")
	}
	o.https.mu.Lock()
	o.https.want = on
	o.https.mu.Unlock()
	if !on {
		if o.closeHTTPS() {
			r.log.Info("tailnet https listener closed", "object", id)
		}
		return nil
	}
	switch st := o.State(); st {
	case StatePending:
		return nil // start() opens it
	case StateReady:
	default:
		return newErr(CodeKilled, "object "+id+" is "+string(st))
	}
	if err := o.openHTTPS(); err != nil {
		o.https.mu.Lock()
		o.https.want = false
		o.https.mu.Unlock()
		return err
	}
	return nil
}

// TailnetHTTPS reports whether o's :443 listener is open.
func (o *Object) TailnetHTTPS() bool {
	o.https.mu.Lock()
	defer o.https.mu.Unlock()
	return o.https.ln != nil
}

// openHTTPSIfWanted is start()'s hook once the node is ready: a failure
// is logged, never fatal to the node (its :80 serves on).
func (o *Object) openHTTPSIfWanted() {
	o.https.mu.Lock()
	want := o.https.want
	o.https.mu.Unlock()
	if !want {
		return
	}
	if err := o.openHTTPS(); err != nil {
		o.reg.log.Warn("tailnet https listener failed", "object", o.id, "err", RedactError(err))
	}
}

// openHTTPS opens the :443 listener on a ready node (idempotent).
func (o *Object) openHTTPS() error {
	s := &o.https
	s.opening.Lock()
	defer s.opening.Unlock()
	o.mu.Lock()
	srv, lc, dnsName, state, ingress := o.srv, o.lc, o.dnsName, o.state, o.httpSrv
	o.mu.Unlock()
	if state != StateReady || srv == nil || ingress == nil {
		return nil // start() opens it once the node is ready
	}
	s.mu.Lock()
	open := s.ln != nil
	s.mu.Unlock()
	if open {
		return nil
	}
	name := trimDot(dnsName)
	certs := o.reg.cfg.HTTPSCert
	real := certs == nil
	if real {
		certs = tailnetCerts
	}
	ctx, cancel := context.WithTimeout(o.reg.ctx, 10*time.Second)
	getCert, err := certs(ctx, lc, name)
	cancel()
	if err != nil {
		return err
	}
	raw, err := srv.Listen("tcp", fmt.Sprintf(":%d", TailnetHTTPSPort))
	if err != nil {
		return err
	}
	ln := tls.NewListener(&trackedListener{Listener: raw, o: o}, &tls.Config{
		GetCertificate: getCert,
		// HTTP/1.1 only, as on :80: upgrades (WebSockets) hijack the conn.
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	})
	hs := &http.Server{
		Handler:           ingress.Handler,
		ReadHeaderTimeout: ingress.ReadHeaderTimeout,
		ErrorLog:          ingress.ErrorLog,
	}
	s.mu.Lock()
	if !s.want || s.ln != nil { // turned off (or opened) meanwhile
		s.mu.Unlock()
		raw.Close()
		return nil
	}
	s.ln, s.hs = ln, hs
	var pctx context.Context
	if real && s.stopPrefetch == nil {
		pctx, s.stopPrefetch = context.WithCancel(context.Background())
	}
	s.mu.Unlock()
	if o.State() != StateReady { // killed meanwhile
		o.closeHTTPS()
		return nil
	}
	go hs.Serve(ln)
	if pctx != nil {
		// A new name's first certificate takes 30–100 s: warm it now, so
		// the first caller's handshake doesn't time out (certprefetch.go).
		PrefetchCert(pctx, lc, name, o.reg.log.With("object", o.id))
	}
	o.reg.log.Info("tailnet https listener open", "object", o.id, "name", name, "addr", raw.Addr().String())
	return nil
}

// closeHTTPS closes the :443 listener; it reports whether one was open.
// Its flows are tracked with the node's others (Kill RSTs them).
func (o *Object) closeHTTPS() bool {
	s := &o.https
	s.mu.Lock()
	ln, hs := s.ln, s.hs
	s.ln, s.hs = nil, nil
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if hs != nil {
		go hs.Close()
	}
	return ln != nil
}

// stopHTTPS is teardown's: close the listener and stop the prefetch.
func (o *Object) stopHTTPS() {
	o.closeHTTPS()
	s := &o.https
	s.mu.Lock()
	stop := s.stopPrefetch
	s.stopPrefetch = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// markTLS sets X-Forwarded-Proto: https on a request that arrived on the
// :443 listener. serveIngress calls it after stripping every client copy.
func markTLS(req *http.Request) {
	if req.TLS != nil {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
}

func trimDot(s string) string {
	if n := len(s); n > 0 && s[n-1] == '.' {
		return s[:n-1]
	}
	return s
}
