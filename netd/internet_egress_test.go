package netd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/netd"
)

// fakeInternet is a hermetic stand-in for the internet path
// (ADR-049): Resolve answers from a fixed table, and
// Dial sends flows to the public test addresses (203.0.113.0/24) to one
// local HTTP server. Any other dial is a guard failure, recorded and
// refused, so a test sees an unchecked address even if the dial "fails".
type fakeInternet struct {
	srv *httptest.Server

	mu       sync.Mutex
	answers  map[string][]netip.Addr
	flip     map[string]int // name -> lookups so far (alternates public/private)
	resolved []string
	dialled  []string
	bad      []string
	flows    []netd.InternetFlow
	seen     []http.Header
}

var testNet = netip.MustParsePrefix("203.0.113.0/24")

func newFakeInternet(t *testing.T) *fakeInternet {
	f := &fakeInternet{answers: map[string][]netip.Addr{}, flip: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Clone())
		f.mu.Unlock()
		w.Header().Set("connection", "close") // every request dials (and is checked) again
		fmt.Fprintf(w, "internet says hi to %s", r.Host)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeInternet) answer(name string, ips ...string) {
	var as []netip.Addr
	for _, s := range ips {
		as = append(as, netip.MustParseAddr(s))
	}
	f.mu.Lock()
	f.answers[name] = as
	f.mu.Unlock()
}

func (f *fakeInternet) config(policy func(string) netd.InternetPolicy) *netd.InternetConfig {
	return &netd.InternetConfig{
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.resolved = append(f.resolved, host)
			if host == "flip.example.com" { // DNS rebinding: public, then loopback
				f.flip[host]++
				if f.flip[host]%2 == 1 {
					return []netip.Addr{netip.MustParseAddr("203.0.113.13")}, nil
				}
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}
			if as, ok := f.answers[host]; ok {
				return as, nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ap, err := netip.ParseAddrPort(addr)
			f.mu.Lock()
			f.dialled = append(f.dialled, addr)
			if err != nil || !testNet.Contains(ap.Addr()) {
				f.bad = append(f.bad, addr)
				f.mu.Unlock()
				return nil, errors.New("fake internet: refusing an address the guard should have stopped: " + addr)
			}
			f.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, "tcp", f.srv.Listener.Addr().String())
		},
		Policy: policy,
		OnFlow: func(fl netd.InternetFlow) {
			f.mu.Lock()
			f.flows = append(f.flows, fl)
			f.mu.Unlock()
		},
	}
}

func (f *fakeInternet) snapshot() (resolved, dialled, bad []string, flows []netd.InternetFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.resolved...), append([]string(nil), f.dialled...), append([]string(nil), f.bad...), append([]netd.InternetFlow(nil), f.flows...)
}

func TestInternetEgress(t *testing.T) {
	inet := newFakeInternet(t)
	inet.answer("api.example.com", "203.0.113.10")
	inet.answer("also.example.com", "203.0.113.11")
	inet.answer("rebind.example.com", "203.0.113.12", "10.0.0.5") // one private answer refuses the name
	inet.answer("smtp.example.com", "203.0.113.20")
	policies := map[string]netd.InternetPolicy{
		"b": {Off: true},                                                                      // tailnet.egress.internet: false
		"c": {Allow: []string{"api.example.com"}},                                             // an allowlist
		"d": {Allow: []string{"api.example.com", "smtp.example.com:25", "203.0.113.0/24:25"}}, // the port policy's override
	}
	e := startEnvWith(t, func(c *netd.Config) {
		c.Internet = inet.config(func(id string) netd.InternetPolicy { return policies[id] })
	}, "a", "b", "c", "d")
	hostIP := firstHostIP(t)

	fetch := func(t *testing.T, obj, u string) (int, string, netd.Error, []byte) {
		t.Helper()
		code, h, b := e.get("http://"+e.objIP[obj]+"/egress?url="+url.QueryEscape(u), nil)
		var er netd.Error
		json.Unmarshal(b, &er)
		return code, h.Get(netd.HeaderError), er, b
	}
	assertNoBadDial := func(t *testing.T) {
		t.Helper()
		if _, _, bad, _ := inet.snapshot(); len(bad) > 0 {
			t.Fatalf("the guard let these addresses reach Dial: %v", bad)
		}
	}

	t.Run("AllowedByDefault", func(t *testing.T) {
		code, te, _, b := fetch(t, "a", "http://api.example.com/hi")
		if code != 200 || te != "" || string(b) != "internet says hi to api.example.com" {
			t.Fatalf("got %d %q %s", code, te, b)
		}
		_, dialled, _, flows := inet.snapshot()
		if len(dialled) == 0 || dialled[len(dialled)-1] != "203.0.113.10:80" {
			t.Fatalf("dialled %v, want the checked address 203.0.113.10:80", dialled)
		}
		var got *netd.InternetFlow
		for i := range flows {
			if flows[i].Object == "a" && flows[i].Host == "api.example.com:80" {
				got = &flows[i]
			}
		}
		if got == nil || got.Addr.String() != "203.0.113.10:80" {
			t.Fatalf("no attributed flow for a → api.example.com:80: %+v", flows)
		}
		// Internet traffic does not carry the platform's identity headers.
		inet.mu.Lock()
		last := inet.seen[len(inet.seen)-1]
		inet.mu.Unlock()
		for k := range last {
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "x-tw-") || strings.HasPrefix(lk, "tw-") || strings.HasPrefix(lk, "tailscale-") || strings.HasPrefix(lk, "cf-") {
				t.Errorf("internet request carried %s", k)
			}
		}
	})

	t.Run("PublicIPLiteral", func(t *testing.T) {
		code, te, _, b := fetch(t, "a", "http://203.0.113.10:8080/")
		if code != 200 || te != "" {
			t.Fatalf("got %d %q %s", code, te, b)
		}
	})

	t.Run("OptOutRefused", func(t *testing.T) {
		rBefore, before, _, _ := inet.snapshot()
		for _, u := range []string{"http://api.example.com/", "http://203.0.113.10/"} {
			code, te, er, b := fetch(t, "b", u)
			if code != 403 || te != netd.CodeInternetEgressUnavailable || er.Object != "b" {
				t.Fatalf("%s: got %d %q %s", u, code, te, b)
			}
			if !strings.Contains(er.Detail, "tailnet.egress.internet") || !strings.Contains(er.Detail, netd.RecipeInternetAPI) {
				t.Errorf("%s: detail %q should name the setting and link the recipe", u, er.Detail)
			}
		}
		if resolved, after, _, _ := inet.snapshot(); len(after) != len(before) || len(resolved) != len(rBefore) {
			t.Fatalf("an opted-out object dialled or resolved: dialled %v resolved %v", after[len(before):], resolved[len(rBefore):])
		}
	})

	t.Run("Allowlist", func(t *testing.T) {
		if code, te, _, b := fetch(t, "c", "http://api.example.com/"); code != 200 || te != "" {
			t.Fatalf("allowlisted: got %d %q %s", code, te, b)
		}
		code, te, er, b := fetch(t, "c", "http://also.example.com/")
		if code != 403 || te != netd.CodeInternetEgressUnavailable || !strings.Contains(er.Detail, "allowlist") {
			t.Fatalf("not allowlisted: got %d %q %s", code, te, b)
		}
		if resolved, _, _, _ := inet.snapshot(); contains(resolved, "also.example.com") {
			t.Error("a name off the allowlist was looked up on the host")
		}
	})

	blocked := []string{
		"127.0.0.1", "::1", "0.0.0.0", // loopback, unspecified
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "fd00::1", // RFC 1918, ULA
		"169.254.169.254", "fe80::1", // link-local, cloud metadata
		"100.64.0.1", "100.100.100.100", "fd7a:115c:a1e0::1", // CGNAT / tailnet
		"::ffff:10.0.0.1", "64:ff9b::a00:1", // translated forms of private addresses
		hostIP, // the host's own address
	}

	t.Run("BlockedRangesViaDNS", func(t *testing.T) {
		for i, ip := range blocked {
			name := fmt.Sprintf("blocked-%d.example.com", i)
			inet.answer(name, ip)
			code, te, er, b := fetch(t, "a", "http://"+name+"/")
			if code != 403 || te != netd.CodeForbiddenDestination || !strings.Contains(er.Detail, "resolves to") {
				t.Errorf("%s → %s: got %d %q %s", name, ip, code, te, b)
			}
		}
		assertNoBadDial(t)
	})

	t.Run("BlockedRangesAsLiterals", func(t *testing.T) {
		for _, ip := range blocked {
			if ip == "100.64.0.1" {
				ip = "100.127.255.254" // the test tailnet's own peers start at 100.64.0.1
			}
			host := ip
			if strings.Contains(ip, ":") {
				host = "[" + ip + "]"
			}
			code, te, _, b := fetch(t, "a", "http://"+host+"/")
			// A tailnet address takes the tailnet path (peer_not_visible when it
			// isn't a peer); everything else is refused by the guard.
			if code != 403 || (te != netd.CodeForbiddenDestination && te != netd.CodePeerNotVisible) {
				t.Errorf("%s: got %d %q %s", ip, code, te, b)
			}
		}
		assertNoBadDial(t)
	})

	t.Run("SpecialNamesNeverResolved", func(t *testing.T) {
		for _, n := range []string{"metadata.google.internal", "printer.local", "router.lan", "foo.localhost"} {
			inet.answer(n, "203.0.113.10") // would succeed if it were ever looked up
			code, te, _, b := fetch(t, "a", "http://"+n+"/")
			if code != 403 || te != netd.CodeForbiddenDestination {
				t.Errorf("%s: got %d %q %s", n, code, te, b)
			}
			if resolved, _, _, _ := inet.snapshot(); contains(resolved, n) {
				t.Errorf("%s was looked up on the host", n)
			}
		}
	})

	t.Run("DNSRebindingRefused", func(t *testing.T) {
		code, te, _, b := fetch(t, "a", "http://rebind.example.com/")
		if code != 403 || te != netd.CodeForbiddenDestination {
			t.Fatalf("mixed public/private answers: got %d %q %s", code, te, b)
		}
		// The name flips between a public and a loopback answer on each
		// lookup: the first fetch is fine, the next dial sees loopback and is
		// refused. The address checked is always the one dialled.
		if code, te, _, b := fetch(t, "a", "http://flip.example.com/"); code != 200 {
			t.Fatalf("first lookup (public): got %d %q %s", code, te, b)
		}
		if code, te, _, b := fetch(t, "a", "http://flip.example.com/"); code != 403 || te != netd.CodeForbiddenDestination {
			t.Fatalf("second lookup (loopback): got %d %q %s", code, te, b)
		}
		assertNoBadDial(t)
	})

	t.Run("TailnetUnchanged", func(t *testing.T) {
		_, before, _, _ := inet.snapshot()
		code, _, b := e.get("http://"+e.objIP["b"]+"/egress?url=http://"+e.echo.IP4+"/", nil)
		if code != 200 {
			t.Fatalf("tailnet egress with internet off: %d %s", code, b)
		}
		for _, u := range []string{"http://ttest-nonexistent/", "http://nope.tail1234.ts.net/"} {
			if code, te, _, b := fetch(t, "a", u); code != 403 || te != netd.CodePeerNotVisible {
				t.Errorf("%s: got %d %q %s", u, code, te, b)
			}
		}
		resolved, after, _, _ := inet.snapshot()
		if len(after) != len(before) {
			t.Errorf("tailnet requests went out the internet path: %v", after[len(before):])
		}
		for _, n := range resolved {
			if !strings.HasSuffix(n, ".example.com") {
				t.Errorf("a tailnet name reached the host resolver: %s", n)
			}
		}
	})

	t.Run("ConnectTunnel", func(t *testing.T) {
		// The Node process tier's CONNECT tunnels take the same path.
		c, err := e.reg.DialEgress(context.Background(), "a", "api.example.com:80")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: api.example.com\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("CONNECT to the internet: %v %v", resp, err)
		}
		resp.Body.Close()
		_, err = e.reg.DialEgress(context.Background(), "b", "api.example.com:80")
		if ne, ok := netd.AsError(err); !ok || ne.Code != netd.CodeInternetEgressUnavailable {
			t.Fatalf("CONNECT with internet off: %v", err)
		}
		_, err = e.reg.DialEgress(context.Background(), "a", "169.254.169.254:80")
		if ne, ok := netd.AsError(err); !ok || ne.Code != netd.CodeForbiddenDestination {
			t.Fatalf("CONNECT to metadata: %v", err)
		}
		assertNoBadDial(t)
	})

	// The default port policy (ADR-052 follow-up, issue #339): TCP 25 to a
	// public address is refused on every internet path that names a port,
	// before DNS and before any dial, unless the workload's allowlist names
	// the destination with that port. Submission ports are untouched.
	t.Run("Port25Refused", func(t *testing.T) {
		rBefore, before, _, _ := inet.snapshot()
		for _, dst := range []string{"smtp.example.com:25", "203.0.113.20:25"} {
			_, err := e.reg.DialEgress(context.Background(), "a", dst) // connect(), node:net, CONNECT
			ne, ok := netd.AsError(err)
			if !ok || ne.Code != netd.CodeInternetPortRefused || ne.Object != "a" || ne.Destination != dst {
				t.Fatalf("connect(%s): %v, want %s", dst, err, netd.CodeInternetPortRefused)
			}
			if !strings.Contains(ne.Detail, "port 25") || !strings.Contains(ne.Detail, "tailnet.egress.internet") || !strings.Contains(ne.Detail, netd.RecipeInternetAPI) {
				t.Errorf("connect(%s): detail %q should name the port, the setting and the recipe", dst, ne.Detail)
			}
			// On the allowlist (object c), but the entry doesn't name the port.
			if _, err := e.reg.DialEgress(context.Background(), "c", "api.example.com:25"); err == nil || !strings.Contains(err.Error(), netd.CodeInternetPortRefused) {
				t.Fatalf("connect(api.example.com:25) from an allowlisted object: %v", err)
			}
		}
		// fetch() to port 25 takes the same path.
		code, te, er, b := fetch(t, "a", "http://smtp.example.com:25/")
		if code != 403 || te != netd.CodeInternetPortRefused || er.Object != "a" {
			t.Fatalf("fetch(http://smtp.example.com:25/): got %d %q %s", code, te, b)
		}
		if resolved, after, _, _ := inet.snapshot(); len(after) != len(before) || len(resolved) != len(rBefore) {
			t.Fatalf("a refused port 25 flow dialled or resolved: dialled %v resolved %v", after[len(before):], resolved[len(rBefore):])
		}
	})

	t.Run("SubmissionPortsProceed", func(t *testing.T) {
		for _, port := range []string{"587", "465"} {
			c, err := e.reg.DialEgress(context.Background(), "a", "smtp.example.com:"+port)
			if err != nil {
				t.Fatalf("connect(smtp.example.com:%s): %v", port, err)
			}
			c.Close()
			if _, dialled, _, _ := inet.snapshot(); !contains(dialled, "203.0.113.20:"+port) {
				t.Fatalf("dialled %v, want the checked address 203.0.113.20:%s", dialled, port)
			}
		}
		assertNoBadDial(t)
	})

	t.Run("Port25Override", func(t *testing.T) {
		// Object d lists "smtp.example.com:25" and "203.0.113.0/24:25".
		for _, dst := range []string{"smtp.example.com:25", "203.0.113.20:25"} {
			c, err := e.reg.DialEgress(context.Background(), "d", dst)
			if err != nil {
				t.Fatalf("connect(%s) with the port listed: %v", dst, err)
			}
			c.Close()
		}
		if _, dialled, _, _ := inet.snapshot(); !contains(dialled, "203.0.113.20:25") {
			t.Fatalf("dialled %v, want 203.0.113.20:25", dialled)
		}
		if code, te, _, b := fetch(t, "d", "http://smtp.example.com:25/"); code != 200 || te != "" {
			t.Fatalf("fetch(http://smtp.example.com:25/) with the port listed: got %d %q %s", code, te, b)
		}
		// An entry that names a port covers that port only.
		_, err := e.reg.DialEgress(context.Background(), "d", "smtp.example.com:587")
		if ne, ok := netd.AsError(err); !ok || ne.Code != netd.CodeInternetEgressUnavailable || !strings.Contains(ne.Detail, "another port") {
			t.Fatalf("connect(smtp.example.com:587) with only :25 listed: %v", err)
		}
		// An entry without a port still doesn't allow 25.
		if _, err := e.reg.DialEgress(context.Background(), "d", "api.example.com:25"); err == nil || !strings.Contains(err.Error(), netd.CodeInternetPortRefused) {
			t.Fatalf("connect(api.example.com:25) with api.example.com listed without a port: %v", err)
		}
		assertNoBadDial(t)
	})
}

func contains(ss []string, s string) bool { return countOf(ss, s) > 0 }

func countOf(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}
