package gen_test

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// connect is the fake host's CONNECT tunnel (hostd connect.go): it checks
// the preamble (a facet's MAC on egress-obj, none on egress-wl), then
// greets with who the socket egresses as and echoes. Destinations named
// refused.test are refused with peer_not_visible.
func (f *fakeHost) connect(w http.ResponseWriter, r *http.Request, facet bool) {
	c, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer c.Close()
	brw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	brw.Flush()
	var p gen.ConnectPreamble
	if err := gen.ReadConnectLine(brw.Reader, &p); err != nil || p.V != 1 {
		return
	}
	as := "workload"
	if facet {
		cls, name, ok := gen.VerifyConnectOnce(f.key, &p, r.Host, time.Now(), nil)
		if !ok {
			gen.WriteConnectLine(c, gen.ConnectStatus{Code: "unknown_object", Message: "bad MAC for " + r.Host})
			return
		}
		as = cls + "/" + name
	} else if p.MAC != "" {
		gen.WriteConnectLine(c, gen.ConnectStatus{Code: "bad_request", Message: "identity on the workload egress"})
		return
	}
	if strings.HasPrefix(r.Host, "refused.test:") {
		gen.WriteConnectLine(c, gen.ConnectStatus{Code: "peer_not_visible", Message: r.Host + " is not in this node's netmap"})
		return
	}
	gen.WriteConnectLine(c, gen.ConnectStatus{OK: true})
	tls := ""
	if p.TLS {
		tls = " tls"
	}
	io.WriteString(c, "hello "+as+" "+r.Host+tls+"\n")
	io.Copy(c, bufio.NewReader(brw.Reader))
}

// TestConnectEgress: connect() from the Worker, a facet-mode and a
// plain-DO object, and ctx.tailnet.connect(), through the generated
// platform and stock workerd to the (fake) host's CONNECT tunnel, each
// with the identity its mode promises (ADR-052).
func TestConnectEgress(t *testing.T) {
	in := start(t, hostdtest.Fixture("compat"), nil)
	cases := []struct {
		where string
		tw    map[string]any
		via   string
		as    string // greeting identity; "" = refused
		code  string
	}{
		{"Worker", wl, "stock", "workload", ""},
		{"Facet", obj("Facet", "f1"), "stock", "Facet/f1", ""},
		{"Plain", obj("Plain", "p1"), "stock", "workload", ""},
		{"Worker", wl, "tailnet", "workload", ""},
		{"Facet", obj("Facet", "f2"), "tailnet", "Facet/f2", ""},
		{"Plain", obj("Plain", "p2"), "tailnet", "", "not_supported"},
	}
	for _, c := range cases {
		t.Run(c.where+"/"+c.via, func(t *testing.T) {
			code, _, r := in.get("/connect?host=db.ttest.ts.net&port=5432&via="+c.via, c.tw)
			if c.as == "" {
				if code != 200 || r["ok"] != false || r["code"] != c.code {
					t.Fatalf("%d %v", code, r)
				}
				return
			}
			want := "hello " + c.as + " db.ttest.ts.net:5432\nping\n"
			if code != 200 || r["ok"] != true || r["read"] != want {
				t.Fatalf("%d %v, want read %q", code, r, want)
			}
		})
	}
	t.Run("RefusedTyped", func(t *testing.T) {
		// ctx.tailnet.connect() rejects opened with the host's code.
		code, _, r := in.get("/connect?host=refused.test&port=1&via=tailnet", obj("Facet", "f3"))
		if code != 200 || r["ok"] != false || r["code"] != "peer_not_visible" {
			t.Fatalf("%d %v", code, r)
		}
		// Stock connect() sees a lost connection (workerd has no way to
		// fail a connect() handler's socket with a reason).
		code, _, r = in.get("/connect?host=refused.test&port=1", obj("Facet", "f3"))
		if code != 200 || r["ok"] != false {
			t.Fatalf("stock: %d %v", code, r)
		}
	})
	t.Run("TLSNetd", func(t *testing.T) {
		code, _, r := in.get("/connect?host=db.ttest.ts.net&port=5432&via=tailnet&tls=1", obj("Facet", "f4"))
		if code != 200 || r["ok"] != true || r["read"] != "hello Facet/f4 db.ttest.ts.net:5432 tls\nping\n" {
			t.Fatalf("%d %v", code, r)
		}
	})
}
