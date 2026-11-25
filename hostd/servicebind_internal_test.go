package hostd

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestEgressDropsTenantBindingWire pins hostd's own half of #347 (ledger
// H-binding-wire), without the platform worker in front of it: a
// HeaderBindingWire on a request that arrives at an HTTP egress handler
// never reaches netd. The workload egress and the Node tier's proxy are
// driven as handlers, with a sink where netd's egress would be; the facet
// egress drops the header in takeBinding, before it looks the object up.
// (hostd.TestServiceBindings drives the whole path, where the platform
// worker has already dropped it.)
func TestEgressDropsTenantBindingWire(t *testing.T) {
	const wire = "v1; entrypoint=Admin; rpc=deleteEverything"
	var got http.Header
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	h := &Host{log: slog.New(slog.DiscardHandler)}
	h.tel = newTelemetry(h)
	w := &workload{h: h, tenantID: "tn", name: "web", tenant: &tenant{id: "tn", egress: sink}, wl: &object{ID: "o1", NetdID: "n1"}}
	inst := &instance{w: w, app: &bundle.App{}, node: &nodeProc{token: "tok"}}

	send := func(t *testing.T, hd http.Handler, hdr ...string) {
		t.Helper()
		got = nil
		r := httptest.NewRequest("GET", "http://peer.example.ts.net/x", nil)
		r.Header.Set("X-Kept", "1")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent || got == nil {
			t.Fatalf("the request did not reach netd's egress: %d %s", rec.Code, rec.Body.String())
		}
		if v := got.Values(HeaderBindingWire); len(v) != 0 {
			t.Fatalf("#347: a tenant-set %s reached netd's egress: %q", HeaderBindingWire, v)
		}
		if got.Get("X-Kept") != "1" {
			t.Fatalf("an ordinary header was dropped: %v", got)
		}
	}

	t.Run("WorkloadEgress", func(t *testing.T) {
		send(t, h.egressWLHandler(inst), HeaderBindingWire, wire)
		// http.Header keys are canonical however the client spelled them.
		send(t, h.egressWLHandler(inst), "tailworks-binding", wire)
	})
	t.Run("NodeProxy", func(t *testing.T) {
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("tw:tok"))
		send(t, h.nodeEgress(inst), "Proxy-Authorization", auth, HeaderBindingWire, wire)
		if got.Get("Proxy-Authorization") != "" {
			t.Fatalf("the proxy credentials reached netd's egress: %v", got)
		}
	})
	t.Run("takeBinding", func(t *testing.T) {
		// No platform binding header: an ordinary egress request.
		r := httptest.NewRequest("GET", "http://peer.example.ts.net/x", nil)
		r.Header.Set(HeaderBindingWire, wire)
		if bc := takeBinding(r); bc != nil || len(r.Header.Values(HeaderBindingWire)) != 0 {
			t.Fatalf("takeBinding left the tenant's header (or invented a call): %v %v", bc, r.Header)
		}
		// A call the platform marked: the tenant's header goes all the same
		// (routeBinding then writes hostd's).
		r = httptest.NewRequest("POST", "http://tw-binding.invalid/", nil)
		r.Header.Set(HeaderBindingWire, wire)
		r.Header.Set(gen.HeaderBinding, "B")
		r.Header.Set(gen.HeaderRPC, "add")
		bc := takeBinding(r)
		if bc == nil || bc.binding != "B" || bc.rpc != "add" {
			t.Fatalf("takeBinding: %+v", bc)
		}
		for _, k := range []string{HeaderBindingWire, gen.HeaderBinding, gen.HeaderRPC} {
			if len(r.Header.Values(k)) != 0 {
				t.Fatalf("takeBinding left %s: %v", k, r.Header)
			}
		}
	})
}
