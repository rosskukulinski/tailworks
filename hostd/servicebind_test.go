package hostd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestServiceBindings: wrangler services between two workloads of one
// tenant (ADR-service-bindings), on stock workerd and the hermetic tailnet.
// svccaller's env.B / env.ADMIN reach svctarget's default export and its
// Admin entrypoint: fetch, a streaming body, a WebSocket, RPC with
// structured-clone values, and the typed refusals. The target names the
// caller (the workload node, or the facet object's node), never a forged
// one. Everything runs with the target on the same host (netd's in-process
// hop; no --allow-sibling-egress) and again after the target moves to
// another host (over the tailnet). The same-host half also runs against
// the real mgmt (its GET .../service-bindings/{binding}).
func TestServiceBindings(t *testing.T) {
	t.Parallel()
	for _, backend := range hostdtest.Backends {
		t.Run(backend, func(t *testing.T) { testServiceBindings(t, backend) })
	}
}

func testServiceBindings(t *testing.T, backend string) {
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
		func(o *hostdtest.Options) { o.Backend = backend })
	e := s.e
	tgt := e.WaitActive("svctarget", e.Deploy(hostdtest.Fixture("svctarget"), "svctarget", nil), 60*time.Second)
	callerDep := e.Deploy(hostdtest.Fixture("svccaller"), "svccaller", map[string]string{"TARGET_HOST": tgt.Hostname})
	cl := e.WaitActive("svccaller", callerDep, 60*time.Second)
	s.waitPeer(cl.IP)
	s.waitPeer(tgt.IP)
	base := "http://" + cl.IP

	// eventually retries while the caller's node learns the target's
	// (the netmap) and the target's entry node is reported ready.
	eventually := func(t *testing.T, path string, ok func(int, map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			code, m := s.getJSON(base + path)
			if ok(code, m) {
				return m
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d %v", path, code, m)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	callerIs := func(m map[string]any, path ...string) string {
		h, _ := get(m, path...).(string)
		return h
	}

	// run is the battery against the target, wherever it runs. tgtHost is
	// the target's entry node now (its name can change when it moves).
	run := func(t *testing.T, where, tgtHost string) {
		t.Run("Fetch", func(t *testing.T) {
			m := eventually(t, "/fetch", func(code int, m map[string]any) bool { return code == 200 })
			if m["entry"] != "default" || m["path"] != "/hello?q=1" {
				t.Fatalf("%s: %v", where, m)
			}
			// Forged cf and x-tw-cf on the tenant's Request never arrive; the
			// tenant's own Tailworks-Binding header is replaced by hostd's.
			if callerIs(m, "caller", "hostname") != cl.Hostname || get(m, "caller", "tw") == nil {
				t.Fatalf("%s: target saw caller %v, want the caller's workload node %s", where, m["caller"], cl.Hostname)
			}
			if m["sawBindingHeader"] != nil {
				t.Fatalf("%s: tenant code saw the binding header: %v", where, m["sawBindingHeader"])
			}
		})
		t.Run("NamedEntrypointFetch", func(t *testing.T) {
			m := eventually(t, "/admin", func(code int, m map[string]any) bool { return code == 200 })
			if m["entry"] != "Admin" || callerIs(m, "caller", "hostname") != cl.Hostname {
				t.Fatalf("%s: %v", where, m)
			}
		})
		t.Run("StreamingBody", func(t *testing.T) {
			code, _, b := s.do("GET", base+"/stream", nil)
			if code != 200 || string(b) != "got:one two three:end" {
				t.Fatalf("%s: %d %q", where, code, b)
			}
		})
		t.Run("WebSocket", func(t *testing.T) {
			code, m := s.getJSON(base + "/ws")
			if code != 200 || m["webSocket"] != true || m["msg"] != "echo:hi" {
				t.Fatalf("%s: %d %v", where, code, m)
			}
		})
		t.Run("RPC", func(t *testing.T) {
			code, m := s.getJSON(base + "/rpc")
			if code != 200 {
				t.Fatalf("%s: %d %v", where, code, m)
			}
			if get(m, "add", "value") != float64(5) {
				t.Errorf("add: %v", m["add"])
			}
			rt, _ := m["roundTrip"].(map[string]any)
			for _, k := range []string{"d", "m", "s", "u8", "big", "nested", "re", "err"} {
				if rt[k] != true {
					t.Errorf("structured clone round trip %s: %v", k, rt)
				}
			}
			if callerIs(m, "whoami", "value", "hostname") != cl.Hostname || get(m, "whoami", "value", "tw") == nil {
				t.Errorf("RPC getCaller(this.ctx): %v", m["whoami"])
			}
			if get(m, "adminStats", "value", "entry") != "Admin" || callerIs(m, "adminStats", "value", "caller", "hostname") != cl.Hostname {
				t.Errorf("named entrypoint RPC: %v", m["adminStats"])
			}
			if get(m, "boom", "name") != "RangeError" || get(m, "boom", "message") != "boom!" {
				t.Errorf("thrown error: %v", m["boom"])
			}
			for _, k := range []string{"fnArg", "streamArg", "fnResult", "targetResult"} {
				if msg, _ := get(m, k, "message").(string); get(m, k, "ok") != false || !strings.Contains(msg, "rpc_not_forwardable") {
					t.Errorf("%s: want rpc_not_forwardable, got %v", k, m[k])
				}
			}
			if msg, _ := get(m, "noMethod", "message").(string); !strings.Contains(msg, "does not implement") {
				t.Errorf("unknown method: %v", m["noMethod"])
			}
			if msg, _ := get(m, "ghost", "message").(string); !strings.Contains(msg, "service_not_found") {
				t.Errorf("binding to a workload that does not exist: %v", m["ghost"])
			}
		})
		t.Run("TargetNotFound", func(t *testing.T) {
			code, h, b := s.do("GET", base+"/ghost", nil)
			if code != 404 || h.Get("tw-error") != "service_not_found" {
				t.Fatalf("%s: %d %q %s", where, code, h.Get("tw-error"), b)
			}
		})
		t.Run("FacetObjectCaller", func(t *testing.T) {
			m := eventually(t, "/object", func(code int, m map[string]any) bool { return code == 200 })
			o := e.WaitObject("svccaller", "Agent", "a1", 30*time.Second)
			if callerIs(m, "fetch", "caller", "hostname") != o.Hostname || callerIs(m, "rpc", "value", "hostname") != o.Hostname {
				t.Fatalf("%s: the facet object's calls should leave as its node %s: %v", where, o.Hostname, m)
			}
		})
		// #347 (ledger H-binding-wire): the header a binding call carries
		// between hosts is hostd's to set. Tenant code that puts it on a plain
		// fetch() (no services binding involved) gets an ordinary request to
		// the target's default fetch(), from the Worker and from a facet
		// object alike. Before the fix each probe ran the Admin entrypoint, or
		// the add() RPC method.
		wireStripped := func(t *testing.T, path, wantCaller string) {
			t.Helper()
			probes := []string{"entrypoint", "rpc", "mixedCase", "viaRequest"}
			m := eventually(t, path+"?host="+tgtHost, func(code int, m map[string]any) bool {
				for _, p := range probes {
					if get(m, p, "status") != float64(200) {
						return false
					}
				}
				return code == 200
			})
			for _, p := range probes {
				if get(m, p, "body", "entry") != "default" || get(m, p, "body", "path") != "/wire" {
					t.Errorf("%s: %s: a plain fetch() with a tenant-set %s header was not an ordinary request: %v", where, p, hostd.HeaderBindingWire, m[p])
				}
				if get(m, p, "body", "sawBindingHeader") != nil {
					t.Errorf("%s: %s: the target's code saw the tenant's wire header: %v", where, p, m[p])
				}
				if callerIs(m, p, "body", "caller", "hostname") != wantCaller {
					t.Errorf("%s: %s: caller %v, want %s", where, p, get(m, p, "body", "caller"), wantCaller)
				}
			}
			if get(m, "rpc", "body", "method") != "POST" {
				t.Errorf("%s: the POST probe should reach fetch() as a POST: %v", where, m["rpc"])
			}
		}
		t.Run("TenantWireHeaderStripped", func(t *testing.T) {
			wireStripped(t, "/wire", cl.Hostname)
		})
		t.Run("TenantWireHeaderStrippedFromFacetObject", func(t *testing.T) {
			o := e.WaitObject("svccaller", "Agent", "a1", 30*time.Second)
			wireStripped(t, "/object-wire", o.Hostname)
		})
		// The limit of that fix, pinned so the docs stay true (ADR-053 §8,
		// docs/security-model.md Known gaps): the callee authorizes a binding
		// call by the caller's node (a tailworks node of this tenant that the
		// tailnet policy lets in), and cannot tell which code on that node
		// wrote the header. Tenant code that speaks HTTP itself on a raw
		// socket to the target (connect(), ADR-052) writes bytes hostd never
		// parses, so its header still counts. If this starts failing, the
		// wire header has become authenticated: update those docs.
		t.Run("RawSocketStillCarriesWireHeader", func(t *testing.T) {
			m := eventually(t, "/raw-wire?host="+tgtHost, func(code int, m map[string]any) bool {
				return code == 200 && m["ok"] == true && get(m, "value", "status") == float64(200)
			})
			if get(m, "value", "body", "entry") != "Admin" || callerIs(m, "value", "body", "caller", "hostname") != cl.Hostname {
				t.Fatalf("%s: a raw socket from a node of the tenant, carrying the wire header: %v (want the Admin entrypoint, called by %s)", where, m, cl.Hostname)
			}
		})
	}

	t.Run("SameHost", func(t *testing.T) {
		run(t, "same host", tgt.Hostname)
		t.Run("PlainTailnetFetch", func(t *testing.T) {
			// No binding: a same-host tailnet fetch() between workloads works
			// too (netd's in-process hop, ADR-019 rule 2 unchanged).
			m := eventually(t, "/plain", func(code int, m map[string]any) bool { return code == 200 })
			if m["entry"] != "default" || callerIs(m, "caller", "hostname") != cl.Hostname {
				t.Fatalf("%v", m)
			}
		})
		t.Run("ForeignCallerRefused", func(t *testing.T) {
			// A tailnet node that is not a tailworks object of this tenant
			// cannot call a named entrypoint or RPC method; a plain request
			// is still a plain request.
			code, h, b := s.do("POST", "http://"+tgt.IP+"/", []byte("[]"), "Tailworks-Binding", "v1; rpc=add")
			if code != 403 || h.Get("tw-error") != "binding_forbidden" {
				t.Fatalf("foreign RPC: %d %q %s", code, h.Get("tw-error"), b)
			}
			code, m := s.getJSON("http://"+tgt.IP+"/x", "Tailworks-Binding", "v1; entrypoint=Admin")
			if code != 403 || m["code"] != "binding_forbidden" {
				t.Fatalf("foreign named entrypoint: %d %v", code, m)
			}
			code, m = s.getJSON("http://" + tgt.IP + "/x")
			if code != 200 || m["entry"] != "default" || callerIs(m, "caller", "nodeId") != s.clientID || get(m, "caller", "tw") != nil {
				t.Fatalf("plain request: %d %v", code, m)
			}
		})
	})

	t.Run("OtherHost", func(t *testing.T) {
		if backend != hostdtest.BackendFake {
			t.Skip("a second host needs fakemgmt (hostdtest.StartHost)")
		}
		h2 := e.StartHost("h2")
		dep2, err := e.FM.MoveWorkload(e.TenantID, "svctarget", "h2")
		if err != nil {
			t.Fatal(err)
		}
		e2 := *e
		e2.Host = h2
		tgt2 := e2.WaitActive("svctarget", dep2, 60*time.Second)
		if tgt2.Hostname == tgt.Hostname {
			t.Logf("target kept its hostname %s on h2", tgt.Hostname)
		}
		run(t, "other host", tgt2.Hostname)
	})
}
