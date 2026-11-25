package gen

import "slices"

// Observability (ADR-045): the tenant service and every facet
// object have the platform's Tail entrypoint as their tail worker, and
// tenant code runs with the nodejs_als compatibility flag, which the
// runtime's trace propagation needs (AsyncLocalStorage, node:async_hooks).

// alsFlags already provide node:async_hooks.
var alsFlags = []string{"nodejs_als", "nodejs_compat", "nodejs_compat_v2"}

// tenantFlags returns the app's compatibility flags with nodejs_als added
// if no flag in alsFlags is there.
func tenantFlags(flags []string) []string {
	out := append([]string{}, flags...)
	for _, f := range flags {
		if slices.Contains(alsFlags, f) {
			return out
		}
	}
	return append(out, "nodejs_als")
}

// tailsLine is the tenant worker's tails: the platform's Tail entrypoint
// with props {kind: "workload"} (a facet's own tails are set by the
// platform when it loads the facet).
const tailsLine = "  tails = [ (name = \"platform\", entrypoint = \"Tail\", props = (json = \"{\\\"kind\\\":\\\"workload\\\"}\")) ],\n"
