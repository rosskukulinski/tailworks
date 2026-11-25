package bundle

import (
	"encoding/json"
	"fmt"
)

// KindBindings reads the bindings of one store kind ("kv", "r2", "d1")
// from a wrangler config (JSON, comments stripped): what `tw kv`, `tw r2`
// and `tw d1` resolve a binding name with. Unlike a deploy it doesn't
// check the names against the app's other bindings. nil when the config
// has none.
func KindBindings(wranglerJSON []byte, kind string) ([]StoreBinding, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(wranglerJSON, &top); err != nil {
		return nil, fmt.Errorf("bundle: wrangler config: %w", err)
	}
	storeKindsMu.RLock()
	defer storeKindsMu.RUnlock()
	for key, k := range storeKinds {
		if k.Kind != kind {
			continue
		}
		raw, ok := top[key]
		if !ok || string(raw) == "null" {
			return nil, nil
		}
		bs, err := k.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("bundle: wrangler config %s: %w", key, err)
		}
		for i := range bs {
			bs[i].Kind = kind
		}
		return bs, nil
	}
	return nil, fmt.Errorf("bundle: no store kind %q", kind)
}
