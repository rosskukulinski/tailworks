package bundle

import (
	"fmt"

	"github.com/rosskukulinski/tailworks/internal/svcbind"
)

// Service bindings (ADR-service-bindings): wrangler.jsonc "services".
// env.<binding> reaches another workload of the same tenant: fetch() and JS
// RPC to its default export or a named WorkerEntrypoint. hostd resolves the
// target through mgmt; the call leaves as the calling node (the workload,
// or the object in facet mode) and a same-host target is delivered in
// process by netd.

// parseServices reads wrangler "services". A service binding may not take
// the name of another binding or a var.
func parseServices(std []byte, a *App) error {
	bs, err := svcbind.FromWrangler(std)
	if err != nil {
		return fmt.Errorf("bundle: wrangler.jsonc %w", err)
	}
	if len(bs) == 0 {
		return nil
	}
	if a.Runtime == RuntimeNode {
		return fmt.Errorf("bundle: services (service bindings) need the workerd runtime")
	}
	taken := map[string]string{}
	for _, b := range a.Bindings {
		taken[b.Name] = "a durable_objects binding"
	}
	for v := range a.Vars {
		taken[v] = "a var"
	}
	for _, s := range a.Stores {
		taken[s.Binding] = "a storage binding"
	}
	if a.Queues != nil {
		for _, p := range a.Queues.Producers {
			taken[p.Binding] = "a queue producer binding"
		}
	}
	for _, w := range a.Workflows {
		taken[w.Binding] = "a workflow binding"
	}
	if a.Assets != nil && a.Assets.Binding != "" {
		taken[a.Assets.Binding] = "the assets binding"
	}
	for _, b := range bs {
		if what, dup := taken[b.Binding]; dup {
			return fmt.Errorf("bundle: wrangler.jsonc services: binding %s has the name of %s", b.Binding, what)
		}
	}
	a.Services = bs
	return nil
}
