package mgmt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestMgmtHostNames(t *testing.T) {
	t.Parallel()
	self := SelfInfo{DNSName: "tw-mgmt-1.t.ts.net.", Tailnet: "t.ts.net"}
	got := NewHostAllowlist(MgmtHostNames(self, "tw-mgmt", "svc:tw-api")...).Names()
	want := []string{"tw-api", "tw-api.t.ts.net", "tw-mgmt", "tw-mgmt-1", "tw-mgmt-1.t.ts.net", "tw-mgmt.t.ts.net"}
	if !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestHostAllowlistAllowed(t *testing.T) {
	t.Parallel()
	a := NewHostAllowlist(MgmtHostNames(SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}, "tw-mgmt", "")...)
	for host, want := range map[string]bool{
		"tw-mgmt.t.ts.net":                  true,
		"TW-MGMT.T.TS.NET":                  true,
		"tw-mgmt.t.ts.net.":                 true,
		"tw-mgmt.t.ts.net:443":              true,
		"tw-mgmt":                           true,
		"tw-mgmt:80":                        true,
		"100.64.0.9":                        true, // tailnet IPv4 (loopback proxy, `tw up`)
		"100.64.0.9:80":                     true,
		"[fd7a:115c:a1e0::9]":               true,
		"[fd7a:115c:a1e0::9]:80":            true,
		"":                                  false,
		"attacker.example":                  false,
		"attacker.example:80":               false,
		"tw-mgmt.attacker.example":          false,
		"tw-mgmt.t.ts.net.attacker.example": false,
		"other.t.ts.net":                    false,
		"localhost":                         false, // no tailnet listener is reached as localhost
	} {
		if got := a.Allowed(host); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", host, got, want)
		}
	}
	var nilList *HostAllowlist
	if !nilList.Allowed("anything") {
		t.Error("a nil allowlist must admit everything (no guard configured)")
	}
}

// TestHostGuardRebinding: a DNS-rebinding page (Host and Origin both the
// attacker's domain, Sec-Fetch-Site same-origin, as the browser would send
// them) is refused with 421 misdirected_request for reads and mutations
// alike, before WhoIs; mgmt's real names and IPs keep working.
func TestHostGuardRebinding(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	self := SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net", IP: "100.64.0.9", NodeID: "nSELF"}
	srv := &Server{M: e.m, Self: self, Version: "test-hostguard", Listeners: []string{"tsnet:80"}}
	srv.Hosts = NewHostAllowlist(MgmtHostNames(self, "tw-mgmt", "")...)
	whoisCalls := 0
	h := srv.TailnetHandler(func(context.Context, string) (WhoIsInfo, error) {
		whoisCalls++
		return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
	}, srv.Handler())
	call := func(method, url string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(""))
		r.RemoteAddr = "100.64.0.6:1"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	rebound := map[string]string{"Origin": "http://attacker.example", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "X-Tw-UI": "1"}
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/tenants"},
		{"GET", "/v1/audit"},
		{"GET", "/ui/"},
		{"POST", "/ui/tenants/tn_x/delete"},
		{"DELETE", "/v1/tenants/tn_x"},
		{"POST", MCPPath},
	} {
		w := call(c.method, "http://attacker.example"+c.path, rebound)
		if w.Code != http.StatusMisdirectedRequest || errCode(w) != "misdirected_request" || !strings.Contains(w.Body.String(), "tw-mgmt.t.ts.net") {
			t.Errorf("rebound %s %s = %d %s, want 421 misdirected_request naming mgmt", c.method, c.path, w.Code, w.Body)
		}
	}
	if whoisCalls != 0 {
		t.Errorf("WhoIs ran %d times for refused requests; the Host check must come first", whoisCalls)
	}
	// The real names (FQDN, short name, the tailnet IP the CLI dials
	// through the loopback proxy, :443) still reach the API and the UI.
	for _, base := range []string{"http://tw-mgmt.t.ts.net", "http://tw-mgmt", "http://100.64.0.9", "https://tw-mgmt.t.ts.net:443", "http://[fd7a:115c:a1e0::9]"} {
		if w := call("GET", base+"/v1/tenants", nil); w.Code != 200 {
			t.Errorf("GET %s/v1/tenants = %d %s", base, w.Code, w.Body)
		}
	}
	if w := call("GET", "http://tw-mgmt.t.ts.net/ui/", map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none"}); w.Code != 200 {
		t.Errorf("GET /ui/ (tw ui) = %d %s", w.Code, w.Body)
	}
}

// TestBrowserGuardOriginAllowlist: the Origin check is tied to the
// allowlist too, so a rebound Host that slips past the listener (a handler
// mounted without TailnetHandler) can't vouch for itself with a matching
// Origin.
func TestBrowserGuardOriginAllowlist(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	g := browserGuard(NewHostAllowlist("tw-mgmt.t.ts.net", "tw-mgmt"), ok)
	post := func(host, origin string) int {
		r := httptest.NewRequest("POST", "http://"+host+"/ui/tenants/tn_x/delete", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w.Code
	}
	if st := post("attacker.example", "http://attacker.example"); st != 403 {
		t.Errorf("rebound Host + matching Origin = %d, want 403", st)
	}
	if st := post("tw-mgmt.t.ts.net", "http://tw-mgmt.t.ts.net"); st != 204 {
		t.Errorf("same-origin form post from mgmt's own name = %d, want 204", st)
	}
	if st := post("tw-mgmt", "http://tw-mgmt"); st != 204 {
		t.Errorf("same-origin form post from the short name = %d, want 204", st)
	}
	if st := post("tw-mgmt.t.ts.net", "http://tw-mgmt"); st != 403 {
		t.Errorf("Origin not equal to Host = %d, want 403", st)
	}
}
