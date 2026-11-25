package hostd

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/svcbind"
)

// Service bindings (ADR-service-bindings).
//
// Caller side: the platform's ServiceBinding entrypoint sends env.B.fetch()
// and env.B.method() through the calling node's egress (the workload's, or
// the facet object's, MAC'd) with gen.HeaderBinding naming the binding
// (and gen.HeaderRPC the method). Only the platform can set them: both
// egress entrypoints strip x-tw-* from tenant requests. hostd resolves the
// binding through mgmt to the target workload's entry node, rewrites the
// request to it and hands it to netd, which sends it as the calling node
// (in process when the target is on this host, samehost.go). The callee
// sees the caller through WhoIs, like any tailnet request: no seal or
// identity is forwarded.
//
// Callee side: a call to a named entrypoint, or an RPC call, carries
// HeaderBindingWire. hostd honours it only from a tailworks object of this
// tenant (its node certificate verified against the tenant's key), only on
// a workload's entry node, and never on a public request; it always strips
// it, and passes {entrypoint, rpc} to the platform in cf.tw.binding.
//
// What authorizes the call is that check plus the tailnet grant
// (ADR-053 §8), not the binding's declaration: the callee cannot tell
// which code on the calling node wrote the header. hostd removes a
// HeaderBindingWire that tenant code set from every HTTP egress request
// (takeBinding, nodeEgress; #347), so a fetch() can't forge a binding
// call. Tenant code that writes HTTP itself on a raw socket to the target
// (connect(), ADR-052; a Node app's CONNECT tunnel) still can: within a
// tenant, a named entrypoint is not a boundary between workloads that the
// tailnet policy lets reach each other (docs/security-model.md, Known gaps).

// HeaderBindingWire carries a binding call's entrypoint and RPC method
// over the tailnet: "v1; entrypoint=<name>; rpc=<method>" (each part
// optional after v1). It is not an identity header (netd forwards it); the
// callee's hostd decides whether it counts (bindingIngress).
const HeaderBindingWire = "Tailworks-Binding"

func init() {
	errStatus["service_not_found"] = 404
	errStatus["binding_forbidden"] = 403
	errStatus["rpc_not_forwardable"] = 400
}

// bindingCall is what the platform asked for on egress.
type bindingCall struct {
	binding, rpc string
}

// takeBinding removes the platform's binding headers from an egress
// request r (nil when there are none). It also removes a HeaderBindingWire
// the tenant's code set (#347): on HTTP egress only routeBinding writes
// that header, for a call the platform marked.
func takeBinding(r *http.Request) *bindingCall {
	r.Header.Del(HeaderBindingWire)
	b, m := r.Header.Get(gen.HeaderBinding), r.Header.Get(gen.HeaderRPC)
	r.Header.Del(gen.HeaderBinding)
	r.Header.Del(gen.HeaderRPC)
	if b == "" {
		return nil
	}
	return &bindingCall{binding: b, rpc: m}
}

// svcTarget is one cached resolution.
type svcTarget struct {
	t   mgmtapi.ServiceBindingTarget
	exp time.Time
}

// svcTargets caches resolutions per (host, tenant, workload, lease epoch,
// binding): a redeploy or a move raises the epoch. A target's entry node
// usually keeps its MagicDNS name when it moves; a host may prefix it
// (-hostname-prefix), so a short TTL and evictOnMiss bound how long calls
// go to a stale name.
var svcTargets sync.Map

const svcTargetTTL = 5 * time.Second

func (h *Host) serviceTarget(ctx context.Context, inst *instance, binding string) (mgmtapi.ServiceBindingTarget, string, error) {
	w := inst.w
	epoch := w.epochNow()
	k := fmt.Sprintf("%p\n%s\n%s\n%d\n%s", h, w.tenantID, w.name, epoch, binding)
	if v, ok := svcTargets.Load(k); ok {
		if c := v.(svcTarget); time.Now().Before(c.exp) {
			return c.t, k, nil
		}
		svcTargets.Delete(k)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	t, err := h.mgmt.ServiceBindingTarget(cctx, w.tenantID, w.name, binding, epoch)
	if err != nil {
		return t, k, err
	}
	svcTargets.Store(k, svcTarget{t: t, exp: time.Now().Add(svcTargetTTL)})
	return t, k, nil
}

// evictOnMiss drops a cached target when netd could not reach it (the
// target moved to a host that names its node differently, or was
// redeployed), so the next call resolves it again.
type evictOnMiss struct {
	http.ResponseWriter
	key string
}

func (e *evictOnMiss) WriteHeader(code int) {
	switch e.Header().Get("tw-error") {
	case "peer_not_visible", "dial_failed", "killed", "object_killed":
		svcTargets.Delete(e.key)
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *evictOnMiss) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (e *evictOnMiss) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// routeBinding rewrites an egress request the platform marked as a binding
// call to the binding's target, and returns the writer to answer it with.
// It answers the request (and returns false) on an error.
func (h *Host) routeBinding(w http.ResponseWriter, r *http.Request, inst *instance, o *object, bc *bindingCall) (http.ResponseWriter, bool) {
	b := svcbind.Find(inst.app.Services, bc.binding)
	if b == nil {
		writeAPIError(w, &apiError{Code: "service_not_found", Detail: "this deployment declares no service binding " + bc.binding})
		return w, false
	}
	if bc.rpc != "" && !svcbind.ValidMethod(bc.rpc) {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "binding " + b.Binding + ": " + bc.rpc + " is not a method an RPC call can name"})
		return w, false
	}
	t, key, err := h.serviceTarget(r.Context(), inst, b.Binding)
	if err != nil {
		writeAPIError(w, asAPIError(err))
		return w, false
	}
	host := strings.TrimSuffix(t.MagicDNS, ".")
	u := *r.URL
	u.Scheme, u.Host = "http", host
	r.URL, r.Host = &u, host
	r.RequestURI = ""
	r.Header.Del(HeaderBindingWire) // takeBinding removed the tenant's; this header is hostd's alone
	if b.Entrypoint != "" || bc.rpc != "" {
		v := "v1"
		if b.Entrypoint != "" {
			v += "; entrypoint=" + b.Entrypoint
		}
		if bc.rpc != "" {
			v += "; rpc=" + bc.rpc
		}
		r.Header.Set(HeaderBindingWire, v)
	}
	return &evictOnMiss{ResponseWriter: w, key: key}, true
}

// parseBindingWire parses HeaderBindingWire.
func parseBindingWire(v string) (entrypoint, rpc string, ok bool) {
	parts := strings.Split(v, ";")
	if strings.TrimSpace(parts[0]) != "v1" {
		return "", "", false
	}
	for _, p := range parts[1:] {
		k, val, found := strings.Cut(strings.TrimSpace(p), "=")
		if !found {
			return "", "", false
		}
		switch k {
		case "entrypoint":
			entrypoint = val
		case "rpc":
			rpc = val
		default:
			return "", "", false
		}
	}
	if !svcbind.ValidEntrypoint(entrypoint) || (rpc != "" && !svcbind.ValidMethod(rpc)) {
		return "", "", false
	}
	return entrypoint, rpc, true
}

// bindingIngress admits a binding call to a named entrypoint or an RPC
// method on o (the callee side), setting route["binding"]. A request
// without HeaderBindingWire is an ordinary request. It answers the request
// (and returns false) on a refusal.
func (h *Host) bindingIngress(w http.ResponseWriter, r *http.Request, o *object, blob, route map[string]any) bool {
	v := r.Header.Get(HeaderBindingWire)
	r.Header.Del(HeaderBindingWire)
	if v == "" {
		return true
	}
	refuse := func(detail string) bool {
		writeAPIError(w, &apiError{Code: "binding_forbidden", Detail: detail})
		return false
	}
	if publicBlob(blob) {
		return refuse("a public request cannot call a service binding entrypoint")
	}
	if o.Class != "" {
		return refuse("a service binding calls a workload's entry node, not object " + o.ID)
	}
	ts, _ := blob["tailscale"].(map[string]any)
	caller, _ := ts["caller"].(map[string]any)
	if caller == nil || caller["tw"] == nil {
		return refuse("only a tailworks workload or object of this tenant can call a named entrypoint or an RPC method (wrangler services); this caller has no node certificate from the tenant")
	}
	ep, rpc, ok := parseBindingWire(v)
	if !ok {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "malformed " + HeaderBindingWire + " header"})
		return false
	}
	route["binding"] = map[string]any{"entrypoint": ep, "rpc": rpc}
	return true
}
