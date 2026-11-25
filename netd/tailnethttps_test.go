package netd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

// selfSigned is a certificate for name and a pool that trusts it: the
// stand-in for the node's ts.net certificate (testcontrol issues none).
func selfSigned(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

type httpsSeen struct {
	proto, caller string
	tls           bool
}

// httpsEnv: one registry whose upstream records what it was handed, one
// object, one client node.
type httpsEnv struct {
	reg    *netd.Registry
	client *nettest.Node
	ip     string
	mu     sync.Mutex
	seen   []httpsSeen
	asked  []string // the names HTTPSCert was asked for
}

func newHTTPSEnv(t *testing.T, certs netd.HTTPSCertFunc, opts netd.ObjectOptions) *httpsEnv {
	t.Helper()
	ctl := startControl(t)
	rundir := newRundir(t)
	e := &httpsEnv{}
	cfg := netd.Config{ControlURL: ctl.URL, StateRoot: filepath.Join(rundir, "state"), Keys: netd.StaticKey(nettest.DummyKey),
		Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var blob netd.CFBlob
			json.Unmarshal([]byte(r.Header.Get(netd.HeaderCF)), &blob)
			e.mu.Lock()
			e.seen = append(e.seen, httpsSeen{proto: r.Header.Get("X-Forwarded-Proto"), caller: blob.Tailscale.Caller.NodeID, tls: r.TLS != nil})
			e.mu.Unlock()
			io.WriteString(w, "ok")
		})}
	if certs != nil {
		cfg.HTTPSCert = func(ctx context.Context, lc *local.Client, name string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
			e.mu.Lock()
			e.asked = append(e.asked, name)
			e.mu.Unlock()
			return certs(ctx, lc, name)
		}
	}
	reg, err := netd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	e.reg = reg
	o, err := reg.Create("h1", opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := o.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	e.ip = o.IPv4().String()
	if e.client, err = nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "https-client", filepath.Join(rundir, "client")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.client.Close() })
	if err := e.client.WaitPeers(ctx, e.ip); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *httpsEnv) last(t *testing.T) httpsSeen {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.seen) == 0 {
		t.Fatal("nothing reached upstream")
	}
	return e.seen[len(e.seen)-1]
}

// get calls the object at scheme://name/ over the tailnet (dialling its IP).
func (e *httpsEnv) get(scheme, name string, pool *x509.CertPool, hdr http.Header) (*http.Response, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			return e.client.Dial(ctx, network, net.JoinHostPort(e.ip, port))
		},
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("GET", scheme+"://"+name+"/x", nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

// A node created with HTTPS serves its attributed ingress on :443 with the
// certificate HTTPSCert hands it, marks those requests (only those) with
// X-Forwarded-Proto: https, keeps :80 as it was, and closes :443 when
// turned off.
func TestTailnetHTTPSListener(t *testing.T) {
	const name = "h1.tailnet.test"
	cert, pool := selfSigned(t, name)
	e := newHTTPSEnv(t, func(context.Context, *local.Client, string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }, nil
	}, netd.ObjectOptions{HTTPS: true})
	o := e.reg.Get("h1")
	if !o.TailnetHTTPS() {
		t.Fatal("HTTPS node is ready, but its :443 listener isn't open")
	}
	e.mu.Lock()
	asked := append([]string(nil), e.asked...)
	e.mu.Unlock()
	if len(asked) != 1 || asked[0] == "" || strings.HasSuffix(asked[0], ".") {
		t.Errorf("HTTPSCert asked for %q, want the node's MagicDNS name once, without the trailing dot", asked)
	}

	// :443: TLS with the injected certificate, WhoIs as on :80, and the
	// client's own X-Forwarded-Proto replaced by netd's.
	resp, err := e.get("https", name, pool, http.Header{"X-Forwarded-Proto": {"gopher"}})
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	if resp.StatusCode != 200 || resp.TLS == nil || resp.TLS.NegotiatedProtocol == "h2" {
		t.Fatalf("https: status %d, tls %v", resp.StatusCode, resp.TLS)
	}
	got := e.last(t)
	if got.proto != "https" || !got.tls || got.caller == "" {
		t.Errorf(":443 request reached upstream as %+v; want proto https, over TLS, with a WhoIs caller", got)
	}

	// :80 is unchanged, and a client can't claim https on it.
	if _, err := e.get("http", name, nil, http.Header{"X-Forwarded-Proto": {"https"}}); err != nil {
		t.Fatalf("http: %v", err)
	}
	if got := e.last(t); got.proto != "" || got.tls || got.caller == "" {
		t.Errorf(":80 request reached upstream as %+v; want no X-Forwarded-Proto (the client's is stripped)", got)
	}

	// Off: :443 refuses connections; :80 still serves.
	if err := e.reg.SetTailnetHTTPS("h1", false); err != nil {
		t.Fatal(err)
	}
	if o.TailnetHTTPS() {
		t.Error("listener still open after SetTailnetHTTPS(false)")
	}
	if _, err := e.get("https", name, pool, nil); err == nil {
		t.Error("https still answers after SetTailnetHTTPS(false)")
	}
	if _, err := e.get("http", name, nil, nil); err != nil {
		t.Errorf("http after turning https off: %v", err)
	}
	// And on again, on the ready node.
	if err := e.reg.SetTailnetHTTPS("h1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.get("https", name, pool, nil); err != nil {
		t.Errorf("https after turning it back on: %v", err)
	}
}

// The default certificate source's check: a node's status must show
// MagicDNS and a certificate domain (a tailnet with HTTPS off, or
// Headscale, has none).
func TestCheckTailnetCerts(t *testing.T) {
	on := &ipnstate.TailnetStatus{MagicDNSEnabled: true}
	for _, c := range []struct {
		name string
		st   *ipnstate.Status
		ok   bool
	}{
		{"certs", &ipnstate.Status{CurrentTailnet: on, CertDomains: []string{"h1.tailnet.ts.net"}}, true},
		{"https off", &ipnstate.Status{CurrentTailnet: on}, false},
		{"magicdns off", &ipnstate.Status{CurrentTailnet: &ipnstate.TailnetStatus{}, CertDomains: []string{"x"}}, false},
	} {
		err := netd.CheckTailnetCerts(c.st)
		if c.ok != (err == nil) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if e, ok := netd.AsError(err); err != nil && (!ok || e.Code != netd.CodeTailnetHTTPSUnavailable) {
			t.Errorf("%s: %v, want %s", c.name, err, netd.CodeTailnetHTTPSUnavailable)
		}
	}
}

// Without HTTPS certificates on the tailnet the listener is refused with
// tailnet_https_unavailable; the node itself is fine and serves :80.
// (testcontrol reports certificate domains it can't actually issue for,
// so the refusal is injected; TestCheckTailnetCerts covers the check.)
func TestTailnetHTTPSUnavailable(t *testing.T) {
	e := newHTTPSEnv(t, func(context.Context, *local.Client, string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
		return nil, netd.ErrNoTailnetCerts("this tailnet issues no HTTPS certificates")
	}, netd.ObjectOptions{HTTPS: true})
	o := e.reg.Get("h1")
	if o.State() != netd.StateReady {
		t.Fatalf("node %s; a missing certificate must not fail the node", o.State())
	}
	if o.TailnetHTTPS() {
		t.Fatal(":443 open on a tailnet without certificates")
	}
	err := e.reg.SetTailnetHTTPS("h1", true)
	ne, ok := netd.AsError(err)
	if !ok || ne.Code != netd.CodeTailnetHTTPSUnavailable || ne.Status() != http.StatusPreconditionFailed {
		t.Fatalf("SetTailnetHTTPS on a tailnet without certificates: %v; want %s (412)", err, netd.CodeTailnetHTTPSUnavailable)
	}
	if !strings.Contains(ne.Detail, "HTTPS certificates") {
		t.Errorf("detail %q doesn't say how to fix it", ne.Detail)
	}
	if o.TailnetHTTPS() {
		t.Error("listener open after a refusal")
	}
	if _, err := e.get("http", "h1.tailnet.test", nil, nil); err != nil {
		t.Errorf(":80 after the refusal: %v", err)
	}
	if err := e.reg.SetTailnetHTTPS("nope", false); err != nil {
		t.Errorf("turning https off on an unknown object: %v, want nil", err)
	}
}
