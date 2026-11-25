package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Naming a store on the command line (issue #458): tw kv --namespace,
// tw r2 <bucket> and tw d1 <db> take the store's name or the binding
// the app in the current directory reaches it by.

// appStores is the app's bindings of one store kind (kv, r2, d1) from its
// wrangler config; nil without an app.
func appStores(a *app, kind string) ([]bundle.StoreBinding, error) {
	if a == nil {
		return nil, nil
	}
	b, err := json.Marshal(a.Wrangler)
	if err != nil {
		return nil, err
	}
	bs, err := bundle.KindBindings(b, kind)
	if err != nil {
		return nil, fail("bad_request", fmt.Sprintf("%s: %v", orAny(a.WranglerFile, "the wrangler config"), err))
	}
	return bs, nil
}

// storeName resolves what the user typed, x, to a store name: (i) a
// store the app declares under that name is that store, (ii) else a
// binding named x is the store it binds, (iii) else x is taken as the
// store's name. So a binding never shadows a store's own name, and a
// name the app doesn't declare means what it always did. The caller
// checks the result is a valid store name.
func storeName(bs []bundle.StoreBinding, x string) string {
	for _, b := range bs {
		if b.Name == x {
			return x
		}
	}
	for _, b := range bs {
		if b.Binding == x {
			return b.Name
		}
	}
	return x
}

// bindingList is "OAUTH_KV -> oauth, CACHE -> CACHE", for an error that
// asks which store.
func bindingList(bs []bundle.StoreBinding) string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Binding + " -> " + b.Name
	}
	return strings.Join(out, ", ")
}
