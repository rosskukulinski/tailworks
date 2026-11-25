package gen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Service bindings (ADR-service-bindings).
//
// Caller side: env.<binding> is a service binding to the platform's
// ServiceBinding entrypoint (props {"binding": name}); a facet's env gets
// the same entrypoint with its class and name, so its calls leave as the
// object. ServiceBinding forwards fetch() and RPC calls to hostd's egress
// with HeaderBinding (and HeaderRPC), which hostd resolves to the target.
//
// Callee side: every tenant main module exports __tw_binding (the
// runtime's bindingEntrypoint): the platform's TENANT_BINDING reaches it
// for a call to a named entrypoint or an RPC method (cf.tw.binding).

//go:embed js/svc.js
var svcJS string

// Headers the platform's ServiceBinding sets on egress (only the platform
// can: both egress entrypoints strip x-tw-* from tenant requests).
const (
	HeaderBinding = "x-tw-binding"
	HeaderRPC     = "x-tw-rpc"
)

// serviceTenantBindings are the tenant worker's env.<binding> entries.
func serviceTenantBindings(app *bundle.App) string {
	var s strings.Builder
	for _, b := range app.Services {
		p, _ := json.Marshal(map[string]string{"binding": b.Binding})
		fmt.Fprintf(&s, "    (name = %s, service = (name = \"platform\", entrypoint = \"ServiceBinding\", props = (json = %s))),\n", cq(b.Binding), cq(string(p)))
	}
	return s.String()
}

// servicePlatformBindings: the platform's binding to the tenant's
// __tw_binding dispatcher (every workload can be a binding's target).
func servicePlatformBindings() string {
	return "    (name = \"TENANT_BINDING\", service = (name = \"tenant\", entrypoint = \"__tw_binding\")),\n"
}

// serviceMainExports is the tenant main module's __tw_binding export.
func serviceMainExports() string {
	return `import { WorkerEntrypoint as __TW_WorkerEntrypoint } from "cloudflare:workers";
const __tw_bind = bindingDispatcher(T, B);
export class __tw_binding extends __TW_WorkerEntrypoint {
  fetch(req) { return __tw_bind.fetch(this.ctx, this.env, req); }
  __tw_call(entrypoint, method, args, cf) { return __tw_bind.call(this.ctx, this.env, entrypoint, method, args, cf); }
}
`
}

// serviceConfig is tw-config.json "services": the bindings a facet's env gets.
type serviceCfg struct {
	Binding string `json:"binding"`
}

func serviceConfig(app *bundle.App) []serviceCfg {
	out := []serviceCfg{}
	for _, b := range app.Services {
		out = append(out, serviceCfg{Binding: b.Binding})
	}
	return out
}
