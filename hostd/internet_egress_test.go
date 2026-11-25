package hostd_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestWorkerInternetEgress (ADR-049): a Worker's
// fetch() to an internet name, end to end through real workerd, hostd and
// netd, with a hermetic stand-in for the internet: api.example.com and
// other.example.com resolve to a documentation address the dialer sends to
// a local server; private.example.com resolves to a private address.
func TestWorkerInternetEgress(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-tw-object") != "" || r.Header.Get("tailscale-user-login") != "" {
			http.Error(w, "identity header leaked", 500)
			return
		}
		fmt.Fprintf(w, "internet stand-in: %s %s", r.Host, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	var mu sync.Mutex
	var dialed []string
	inet := &hostd.ContainerInternet{
		HostAddrs: []netip.Addr{},
		Resolve: func(_ context.Context, h string) ([]netip.Addr, error) {
			switch h {
			case "api.example.com", "other.example.com":
				return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
			case "private.example.com":
				return []netip.Addr{netip.MustParseAddr("192.168.1.10")}, nil
			}
			return nil, fmt.Errorf("no such host")
		},
		Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, a)
			mu.Unlock()
			if a != "203.0.113.7:80" && a != "203.0.113.7:25" {
				return nil, fmt.Errorf("unreachable")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		},
	}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.WorkerInternet = inet
	})
	e := s.e

	// deploy runs the hello template as workload with UPSTREAM=upstream and
	// the given tailnet.egress.internet ("" = absent).
	deploy := func(t *testing.T, workload, upstream, internet string) hostd.ObjectStatus {
		t.Helper()
		files, err := hostdtest.Build(hostdtest.Template("hello"), map[string]string{"UPSTREAM": upstream})
		if err != nil {
			t.Fatal(err)
		}
		egress := ""
		if internet != "" {
			egress = `, "egress": {"internet": ` + internet + `}`
		}
		files["tailworks.jsonc"] = []byte(`{"name": "` + workload + `", "tailnet": {"tags": ["tag:tw-wl-hello"]` + egress + `}}`)
		wl := e.WaitActive(workload, e.DeployFiles(files, workload), 60*time.Second)
		s.waitPeer(wl.IP)
		return wl
	}
	upstream := func(t *testing.T, wl hostd.ObjectStatus) (status float64, twErr string, body string) {
		t.Helper()
		code, r := s.getJSON("http://" + wl.IP + "/")
		if code != 200 {
			t.Fatalf("worker: %d %v", code, r)
		}
		status, _ = get(r, "upstream", "status").(float64)
		twErr, _ = get(r, "upstream", "error").(string)
		switch b := get(r, "upstream", "body").(type) {
		case string:
			body = b
		case map[string]any:
			body = fmt.Sprint(b)
		}
		return status, twErr, body
	}

	t.Run("OnByDefault", func(t *testing.T) {
		wl := deploy(t, "inet-on", "http://api.example.com/zen", "")
		st, te, body := upstream(t, wl)
		if st != 200 || te != "" || body != "internet stand-in: api.example.com /zen" {
			t.Fatalf("fetch(api.example.com): %v %q %q", st, te, body)
		}
		// The first flow per destination is audited as egress, attributed to
		// the object, and says it left from this host's network.
		deadline := time.Now().Add(10 * time.Second)
		for {
			found := false
			for _, rec := range e.Audit(wl.ID).Items {
				if rec.Kind == "egress" && rec.Dst == "api.example.com:80" && strings.Contains(string(rec.Detail), "internet") {
					found = true
				}
			}
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no egress audit record for the internet flow")
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("OptOut", func(t *testing.T) {
		wl := deploy(t, "inet-off", "http://api.example.com/zen", "false")
		mu.Lock()
		before := len(dialed)
		mu.Unlock()
		st, te, body := upstream(t, wl)
		if st != 403 || te != netd.CodeInternetEgressUnavailable {
			t.Fatalf("internet: false: %v %q %q", st, te, body)
		}
		mu.Lock()
		after := len(dialed)
		mu.Unlock()
		if after != before {
			t.Fatal("an opted-out Worker dialled the internet")
		}
	})

	t.Run("Allowlist", func(t *testing.T) {
		if st, te, body := upstream(t, deploy(t, "inet-list", "http://api.example.com/", `["api.example.com"]`)); st != 200 {
			t.Fatalf("allowlisted: %v %q %q", st, te, body)
		}
		if st, te, body := upstream(t, deploy(t, "inet-list2", "http://other.example.com/", `["api.example.com"]`)); st != 403 || te != netd.CodeInternetEgressUnavailable {
			t.Fatalf("not allowlisted: %v %q %q", st, te, body)
		}
	})

	t.Run("SSRFGuard", func(t *testing.T) {
		for name, u := range map[string]string{
			"inet-dns-private": "http://private.example.com/",
			"inet-metadata":    "http://169.254.169.254/latest/meta-data/",
			"inet-rfc1918":     "http://10.1.2.3/",
		} {
			if st, te, body := upstream(t, deploy(t, name, u, "")); st != 403 || te != netd.CodeForbiddenDestination {
				t.Errorf("%s: %v %q %q", u, st, te, body)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, a := range dialed {
			if a != "203.0.113.7:80" {
				t.Errorf("dialled %s past the guard", a)
			}
		}
	})

	// The default port policy (ADR-052 follow-up, issue #339): port 25 is
	// refused with internet_port_refused and audited as deny; an allowlist
	// entry that names the port allows it.
	t.Run("Port25", func(t *testing.T) {
		wl := deploy(t, "inet-smtp", "http://api.example.com:25/", "")
		mu.Lock()
		before := len(dialed)
		mu.Unlock()
		st, te, body := upstream(t, wl)
		if st != 403 || te != netd.CodeInternetPortRefused {
			t.Fatalf("fetch(api.example.com:25): %v %q %q", st, te, body)
		}
		mu.Lock()
		after := len(dialed)
		mu.Unlock()
		if after != before {
			t.Fatal("a refused port 25 flow was dialled")
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			found := false
			for _, rec := range e.Audit(wl.ID).Items {
				if rec.Kind == "deny" && rec.Dst == "api.example.com:25" && rec.Code == netd.CodeInternetPortRefused {
					found = true
				}
			}
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no deny audit record with %s for api.example.com:25", netd.CodeInternetPortRefused)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if st, te, body := upstream(t, deploy(t, "inet-smtp-ok", "http://api.example.com:25/", `["api.example.com:25"]`)); st != 200 || body != "internet stand-in: api.example.com:25 /" {
			t.Fatalf("fetch(api.example.com:25) with the port listed: %v %q %q", st, te, body)
		}
	})
}
