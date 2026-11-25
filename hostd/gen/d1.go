package gen

import _ "embed"

// D1 (ADR-038): a d1_databases binding is a platform store of kind "d1".
// Tenant code gets workerd's own D1 API (the wrapped binding
// "cloudflare-internal:d1-api"); its inner fetcher is the platform's
// D1Backend entrypoint (js/store-d1.js) with props {kind, name}. A facet
// can't be handed a wrapped binding, so its env gets a stub to D1Backend
// and the facet runtime puts the same API around it (the "d1" shim).

//go:embed js/store-d1.js
var storeD1JS string

func init() {
	RegisterStoreKind(StoreKindGen{
		Kind:          "d1",
		Binding:       "wrapped",
		Backend:       "D1Backend",
		JS:            storeD1JS,
		WrappedModule: "cloudflare-internal:d1-api",
		FacetWrap:     "d1",
	})
}
