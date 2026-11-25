package bundle

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Platform storage bindings (ADR-044): wrangler.jsonc keys such
// as kv_namespaces whose data lives in a platform-owned store. Each
// binding type registers a StoreKind (KV in kv.go; D1, Queues and R2 are
// expected to follow); the parser finds every registered key in
// wrangler.jsonc and turns its entries into StoreBindings.

// StoreBinding is one storage binding of the app: env.<Binding> reaches
// the store (Kind, Name) of the workload.
type StoreBinding struct {
	Kind    string // e.g. "kv"
	Binding string // the env name
	Name    string // the store's name within the workload and kind
}

// StoreKind is a binding type backed by a platform store.
type StoreKind struct {
	Kind        string // store kind ([a-z][a-z0-9]{0,15}), as in the API
	WranglerKey string // the wrangler.jsonc key, e.g. "kv_namespaces"
	// Parse turns the key's value into bindings (Kind is filled in by the
	// caller). Binding and store names are checked by the caller.
	Parse func(raw json.RawMessage) ([]StoreBinding, error)
}

var (
	storeKindsMu sync.RWMutex
	storeKinds   = map[string]StoreKind{} // by wrangler key
)

// RegisterStoreKind registers a storage binding type. It panics on a
// duplicate kind or key (a programming error).
func RegisterStoreKind(k StoreKind) {
	storeKindsMu.Lock()
	defer storeKindsMu.Unlock()
	if !storeKindNameRE.MatchString(k.Kind) || k.WranglerKey == "" || k.Parse == nil {
		panic("bundle: bad store kind " + k.Kind)
	}
	for _, o := range storeKinds {
		if o.Kind == k.Kind || o.WranglerKey == k.WranglerKey {
			panic("bundle: store kind registered twice: " + k.Kind)
		}
	}
	storeKinds[k.WranglerKey] = k
}

var (
	storeKindNameRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	// StoreNameRE is a valid store name (as mgmt's API path element).
	StoreNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// parseStores reads every registered storage binding key of the stripped
// wrangler JSON. Bindings are in (key, entry) order; a binding name may
// not repeat or collide with a Durable Object binding or a var.
func parseStores(wranglerJSON []byte, a *App) ([]StoreBinding, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(wranglerJSON, &top); err != nil {
		return nil, fmt.Errorf("bundle: wrangler.jsonc: %w", err)
	}
	storeKindsMu.RLock()
	keys := make([]string, 0, len(storeKinds))
	for k := range storeKinds {
		if _, ok := top[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	kinds := make([]StoreKind, len(keys))
	for i, k := range keys {
		kinds[i] = storeKinds[k]
	}
	storeKindsMu.RUnlock()

	taken := map[string]string{}
	for _, b := range a.Bindings {
		taken[b.Name] = "a durable_objects binding"
	}
	for v := range a.Vars {
		taken[v] = "a var"
	}
	var out []StoreBinding
	for _, k := range kinds {
		bs, err := k.Parse(top[k.WranglerKey])
		if err != nil {
			return nil, fmt.Errorf("bundle: wrangler.jsonc %s: %w", k.WranglerKey, err)
		}
		for _, b := range bs {
			b.Kind = k.Kind
			if !identRE.MatchString(b.Binding) || strings.HasPrefix(b.Binding, "__TW") {
				return nil, fmt.Errorf("bundle: wrangler.jsonc %s: bad binding name %q", k.WranglerKey, b.Binding)
			}
			if !StoreNameRE.MatchString(b.Name) {
				return nil, fmt.Errorf("bundle: wrangler.jsonc %s: binding %s: store name %q must match %s", k.WranglerKey, b.Binding, b.Name, StoreNameRE)
			}
			if what, dup := taken[b.Binding]; dup {
				return nil, fmt.Errorf("bundle: wrangler.jsonc %s: binding %s has the name of %s", k.WranglerKey, b.Binding, what)
			}
			taken[b.Binding] = "another storage binding"
			out = append(out, b)
		}
	}
	return out, nil
}

// StoreKindNames lists the registered store kinds, sorted. Each is a
// placement feature (internal/features).
func StoreKindNames() []string {
	storeKindsMu.RLock()
	defer storeKindsMu.RUnlock()
	out := make([]string, 0, len(storeKinds))
	for _, k := range storeKinds {
		out = append(out, k.Kind)
	}
	sort.Strings(out)
	return out
}
