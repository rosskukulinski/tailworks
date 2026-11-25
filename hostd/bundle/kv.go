package bundle

import (
	"encoding/json"
	"fmt"
)

// Workers KV (ADR-037): wrangler.jsonc "kv_namespaces" entries
// become platform stores of kind "kv".
//
//	"kv_namespaces": [{ "binding": "OAUTH_KV", "id": "oauth" }]
//
// The store's name is the entry's "id" (a namespace the operator created
// with `tw kv namespace create`, or any name: the first write creates
// it), or the binding name when there is no id. Bindings with the same id
// share one namespace. "preview_id" and "remote" are wrangler dev's and
// are ignored.
func init() {
	RegisterStoreKind(StoreKind{Kind: "kv", WranglerKey: "kv_namespaces", Parse: parseKVNamespaces})
}

func parseKVNamespaces(raw json.RawMessage) ([]StoreBinding, error) {
	var in []struct {
		Binding string `json:"binding"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make([]StoreBinding, len(in))
	for i, b := range in {
		if b.Binding == "" {
			return nil, fmt.Errorf("entry %d has no binding", i)
		}
		name := b.ID
		if name == "" {
			name = b.Binding
		}
		out[i] = StoreBinding{Binding: b.Binding, Name: name}
	}
	return out, nil
}
