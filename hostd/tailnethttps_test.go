package hostd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd"
	"tailscale.com/client/local"
)

// tlsEchoJS answers with what the Worker was handed.
const tlsEchoJS = `export default {
  async fetch(req) {
    return Response.json({
      url: req.url,
      proto: req.headers.get("x-forwarded-proto"),
      cf: req.cf ? Object.keys(req.cf).sort() : null,
      method: req.method,
      body: req.method === "POST" ? await req.text() : null,
    });
  },
};
`

func tlsApp(name string, https bool) map[string][]byte {
	h := ""
	if https {
		h = `, "https": true`
	}
	return map[string][]byte{
		"wrangler.jsonc":  []byte(`{"name": "` + name + `", "main": "index.js", "compatibility_date": "2026-04-01"}`),
		"index.js":        []byte(tlsEchoJS),
		"tailworks.jsonc": []byte(`{"name": "` + name + `", "tailnet": {"tags": ["tag:ttest"]` + h + `}}`),
	}
}

// testCert is a self-signed certificate for every name, and a pool that
// trusts it.
func testCert(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tw-test"},
		DNSNames: []string{"*.ttest.ts.net", "tlsapp.ttest.ts.net"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// TestTailnetHTTPS: a workload with tailnet.https serves its node on :443
// too, and the Worker sees an https:// request.url (and the same cf) for
// requests that came that way, http:// for :80 as before. Turning it off
// closes :443; a tailnet without certificates fails the deploy with
// tailnet_https_unavailable and leaves the active deployment serving.
func TestTailnetHTTPS(t *testing.T) {
	t.Parallel()
	cert, pool := testCert(t)
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) {
		o.TailnetHTTPSCert = func(_ context.Context, _ *local.Client, name string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
			if strings.HasPrefix(name, "nocert") {
				return nil, &netd.Error{Code: netd.CodeTailnetHTTPSUnavailable, Detail: "this tailnet issues no HTTPS certificates"}
			}
			return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }, nil
		}
	})
	e := s.e
	call := func(wl hostd.ObjectStatus, scheme, path string) (map[string]any, error) {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return s.client.Dial(ctx, network, net.JoinHostPort(wl.IP, port))
			},
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}
		defer tr.CloseIdleConnections()
		hc := &http.Client{Transport: tr, Timeout: 20 * time.Second}
		u := scheme + "://tlsapp.ttest.ts.net" + path
		var resp *http.Response
		var err error
		if body, ok := strings.CutPrefix(path, "/post/"); ok {
			resp, err = hc.Post(u, "text/plain", strings.NewReader(body))
		} else {
			resp, err = hc.Get(u)
		}
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%d %s: %w", resp.StatusCode, b, err)
		}
		return m, nil
	}

	wl := e.WaitActive("tlsapp", e.DeployFiles(tlsApp("tlsapp", true), "tlsapp"), 90*time.Second)
	s.waitPeer(wl.IP)
	r, err := call(wl, "https", "/a/b?c=1")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	if r["url"] != "https://tlsapp.ttest.ts.net/a/b?c=1" || r["proto"] != "https" {
		t.Fatalf("over :443 the Worker saw %v; want an https:// url and X-Forwarded-Proto: https", r)
	}
	h, err := call(wl, "http", "/a/b?c=1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	if h["url"] != "http://tlsapp.ttest.ts.net/a/b?c=1" || h["proto"] != nil {
		t.Fatalf("over :80 the Worker saw %v; want the http:// url as before", h)
	}
	if rc, hc := js(r["cf"]), js(h["cf"]); rc != hc || !strings.Contains(rc, "tailscale") {
		t.Fatalf("request.cf over :443 %s differs from :80 %s", rc, hc)
	}
	// A body survives the URL rewrite.
	if p, err := call(wl, "https", "/post/hello"); err != nil || p["method"] != "POST" || p["body"] != "hello" || p["url"] != "https://tlsapp.ttest.ts.net/post/hello" {
		t.Fatalf("https POST: %v %v", p, err)
	}

	// Redeploy without it: :443 closes, :80 serves on.
	wl = e.WaitActive("tlsapp", e.DeployFiles(tlsApp("tlsapp", false), "tlsapp"), 90*time.Second)
	if _, err := call(wl, "https", "/"); err == nil {
		t.Fatal("https still answers after a deploy without tailnet.https")
	}
	if h, err := call(wl, "http", "/"); err != nil || h["url"] != "http://tlsapp.ttest.ts.net/" {
		t.Fatalf("http after turning https off: %v %v", h, err)
	}

	// No certificates on the tailnet: the deploy fails with the code, and
	// the deployment that was active keeps serving.
	nwl := e.WaitActive("nocert", e.DeployFiles(tlsApp("nocert", false), "nocert"), 90*time.Second)
	e.DeployFiles(tlsApp("nocert", true), "nocert")
	deadline := time.Now().Add(90 * time.Second)
	var ws *hostd.WorkloadStatus
	for {
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == "nocert" && w.Status == "failed" { // (Status reports the active deployment's ID)
				ws = &w
			}
		}
		if ws != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ws == nil || !strings.Contains(ws.Error, netd.CodeTailnetHTTPSUnavailable) {
		t.Fatalf("deploy on a tailnet without certificates: %+v; want failed with %s", ws, netd.CodeTailnetHTTPSUnavailable)
	}
	s.waitPeer(nwl.IP)
	if code, m := s.getJSON("http://" + nwl.IP + "/"); code != 200 || !strings.HasPrefix(m["url"].(string), "http://") {
		t.Fatalf("the active deployment stopped serving after the refused one: %d %v", code, m)
	}
}
