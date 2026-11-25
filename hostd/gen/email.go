package gen

import (
	"fmt"

	"github.com/rosskukulinski/tailworks/internal/emailstub"
)

// Email is not supported (docs/COMPAT.md "Email"), but importing
// `cloudflare:email` must work however the Worker was bundled. The built-in
// bundler stubs the import itself (internal/esbundle); a pre-built
// `no_bundle` Worker (a Vite build, issue #354) still imports it, and stock
// workerd's `cloudflare:email` imports an internal module,
// `cloudflare-internal:email`, that only production workerd builds in.
// Without one of the two stubs below the Worker fails to start.

// emailFile is the instance file holding the cloudflare-internal:email stub.
const emailFile = "tw-email.js"

// emailExtension is the config's `extensions` line, which gives the tenant
// service (and the platform) `cloudflare-internal:email`. `internal` keeps
// tenant code from importing it by that name.
func emailExtension() string {
	return fmt.Sprintf("  extensions = [ (modules = [ (name = %s, esModule = embed %s, internal = true) ]) ],\n",
		cq(emailstub.InternalModule), cq(emailFile))
}

// emailFacetModule is the facets' Dynamic Worker module map entry for
// `cloudflare:email`. Config extensions don't reach Dynamic Workers, so
// there the module itself is the stub: a module of the loaded Worker named
// `cloudflare:email` takes the import before workerd's built-in does.
func emailFacetModule() string {
	return fmt.Sprintf("  \"cloudflare:email\": { js: %s },\n", jsq(emailstub.ClassJS))
}
