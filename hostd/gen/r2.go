package gen

import _ "embed"

// R2 buckets (ADR-036): a platform store kind whose tenant binding
// is workerd's native r2Bucket, served by the platform's R2Backend
// (js/store-r2.js); facets get R2FacetProxy through a shim that rebuilds
// R2Object / R2ObjectBody (js/store-r2-facet.js).

//go:embed js/store-r2.js
var r2JS string

//go:embed js/store-r2-facet.js
var r2FacetJS string

func init() {
	RegisterStoreKind(StoreKindGen{
		Kind:       "r2",
		Binding:    "r2Bucket",
		Backend:    "R2Backend",
		FacetProxy: "R2FacetProxy",
		JS:         r2JS,
		FacetShim:  r2FacetJS,
	})
}
