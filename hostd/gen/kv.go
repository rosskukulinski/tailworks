package gen

import _ "embed"

// Workers KV (ADR-037): a kv_namespaces binding is workerd's own
// kvNamespace binding targeting the platform's KVBackend (js/kv.js), on the
// platform-store substrate; facets get KVFacet.

//go:embed js/kv.js
var kvJS string

func init() {
	RegisterStoreKind(StoreKindGen{Kind: "kv", Binding: "kvNamespace", Backend: "KVBackend", FacetProxy: "KVFacet", JS: kvJS})
}
