package gen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Platform stores (ADR-044): every storage binding of the app
// (bundle.App.Stores) is backed by the platform's `Store` Durable Object
// class (js/store.js) in the platform worker, one object per (kind, name).
//
// Each kind registers a StoreKindGen: the workerd binding type its tenant
// binding uses (workerd's own `kvNamespace`, `queue`, `r2Bucket`, … so
// tenant code gets upstream's API, or a plain `service`), the backend
// entrypoint that binding targets (with props {kind, name}), and its
// platform module. Facets (Dynamic Workers) can't be handed workerd's
// native bindings, so a kind with a native binding also names a facet
// proxy entrypoint: the facet's env.<Binding> is an RPC stub to it, and it
// forwards each call to a native binding of the platform worker
// (`__TW_STORE_<Binding>`), so facets get the same semantics.

//go:embed js/store.js
var storeJS string

// StoreKindGen is how a store kind is generated.
type StoreKindGen struct {
	Kind string
	// Binding is the workerd binding type of env.<Binding> in the tenant
	// worker: "kvNamespace", "queue", "r2Bucket", "service", or "wrapped"
	// (a wrapped binding of WrappedModule whose inner "fetcher" is a
	// service binding to Backend: workerd's D1 API).
	Binding string
	// Backend is the platform entrypoint class the binding targets.
	Backend string
	// FacetProxy is the platform entrypoint class a facet's env gets
	// (props {"binding": name}); it calls env["__TW_STORE_" + binding].
	// Empty: facets get Backend itself (only for Binding "service").
	FacetProxy string
	// JS is the kind's platform module (tw-store-<kind>.js). It imports
	// from "tw-store.js" and exports Backend (and FacetProxy).
	JS string

	// WrappedModule is the module of a "wrapped" binding, e.g.
	// "cloudflare-internal:d1-api".
	WrappedModule string
	// FacetWrap names the shim the facet runtime (tw-rt.js) puts around
	// the facet's stub to Backend, for a "wrapped" binding (which can't be
	// handed to a Dynamic Worker): "d1".
	FacetWrap string

	// FacetShim, if set, is a module for facets (it runs in the facet,
	// with tenant code) exporting wrap(stub): a facet's env.<Binding>
	// becomes wrap(<the RPC stub to FacetProxy>). For kinds whose API
	// returns objects with methods, which can't cross RPC (R2's
	// R2ObjectBody).
	FacetShim string
}

var (
	storeGenMu sync.RWMutex
	storeGens  = map[string]StoreKindGen{}
	classRE    = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// RegisterStoreKind registers how a store kind is generated. It panics on
// a duplicate or malformed kind (a programming error).
func RegisterStoreKind(k StoreKindGen) {
	storeGenMu.Lock()
	defer storeGenMu.Unlock()
	switch k.Binding {
	case "kvNamespace", "queue", "r2Bucket":
		if !classRE.MatchString(k.FacetProxy) {
			panic("gen: store kind " + k.Kind + ": a native binding needs a FacetProxy")
		}
	case "service":
		if k.FacetProxy != "" && !classRE.MatchString(k.FacetProxy) {
			panic("gen: store kind " + k.Kind + ": bad FacetProxy")
		}
	case "wrapped":
		if k.WrappedModule == "" || k.FacetWrap == "" || k.FacetProxy != "" {
			panic("gen: store kind " + k.Kind + ": a wrapped binding needs WrappedModule and FacetWrap, and no FacetProxy")
		}
	default:
		panic("gen: store kind " + k.Kind + ": bad binding type " + k.Binding)
	}
	if !classRE.MatchString(k.Backend) || k.JS == "" {
		panic("gen: store kind " + k.Kind + ": bad backend or no JS")
	}
	if _, dup := storeGens[k.Kind]; dup {
		panic("gen: store kind registered twice: " + k.Kind)
	}
	storeGens[k.Kind] = k
}

func storeGen(kind string) (StoreKindGen, bool) {
	storeGenMu.RLock()
	defer storeGenMu.RUnlock()
	k, ok := storeGens[kind]
	return k, ok
}

// storeCfg is one entry of tw-config.json "stores": what a facet's env
// gets for a binding.
type storeCfg struct {
	Binding string         `json:"binding"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Facet   string         `json:"facet"` // platform entrypoint class
	Props   map[string]any `json:"props"`

	// Wrap is the facet runtime's shim around the stub (StoreKindGen.FacetWrap).
	Wrap string `json:"wrap,omitempty"`
}

// checkStores refuses a binding of an unknown kind, and a secret with a
// storage binding's name.
func checkStores(app *bundle.App) error {
	for _, b := range app.Stores {
		if _, ok := storeGen(b.Kind); !ok {
			return fmt.Errorf("gen: storage binding %s: kind %q is not supported by this hostd", b.Binding, b.Kind)
		}
		if _, ok := app.Secrets[b.Binding]; ok {
			return fmt.Errorf("gen: secret %q has the name of a storage binding; rename one (tw secret rm %s)", b.Binding, b.Binding)
		}
	}
	return nil
}

// storeKinds are the kinds app uses, sorted.
func storeKinds(app *bundle.App) []StoreKindGen {
	seen := map[string]bool{}
	var out []StoreKindGen
	for _, b := range app.Stores {
		if !seen[b.Kind] {
			seen[b.Kind] = true
			k, _ := storeGen(b.Kind)
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func storeProps(b bundle.StoreBinding) string {
	p, _ := json.Marshal(map[string]string{"kind": b.Kind, "name": b.Name})
	return string(p)
}

// storeConfig is tw-config.json "stores".
func storeConfig(app *bundle.App) []storeCfg {
	out := []storeCfg{}
	for _, b := range app.Stores {
		k, _ := storeGen(b.Kind)
		c := storeCfg{Binding: b.Binding, Kind: b.Kind, Name: b.Name, Facet: k.Backend, Props: map[string]any{"kind": b.Kind, "name": b.Name}, Wrap: k.FacetWrap}
		if k.FacetProxy != "" {
			c.Facet, c.Props = k.FacetProxy, map[string]any{"binding": b.Binding}
		}
		out = append(out, c)
	}
	return out
}

// storeFiles are the platform modules for app's stores (none without).
func storeFiles(app *bundle.App) map[string]string {
	if len(app.Stores) == 0 {
		return nil
	}
	files := map[string]string{"tw-store.js": storeJS}
	var idx strings.Builder
	idx.WriteString("export { Store } from \"tw-store.js\";\n")
	for _, k := range storeKinds(app) {
		mod := "tw-store-" + k.Kind + ".js"
		files[mod] = k.JS
		cls := []string{k.Backend}
		if k.FacetProxy != "" && k.FacetProxy != k.Backend {
			cls = append(cls, k.FacetProxy)
		}
		fmt.Fprintf(&idx, "export { %s } from %s;\n", strings.Join(cls, ", "), jsq(mod))
	}
	files["tw-stores.js"] = idx.String()
	return files
}

// storePlatformExports replaces platform.js's //@@STORES@@ marker.
func storePlatformExports(app *bundle.App) string {
	if len(app.Stores) == 0 {
		return ""
	}
	return "export * from \"tw-stores.js\";\n"
}

// storeModules are the platform worker's module entries.
func storeModules(app *bundle.App) string {
	files := storeFiles(app)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "    (name = %s, esModule = embed %s),\n", cq(n), cq(n))
	}
	return b.String()
}

// storeNamespace is the platform's Store DO namespace entry.
func storeNamespace(app *bundle.App, workload string) string {
	if len(app.Stores) == 0 {
		return ""
	}
	return fmt.Sprintf("    (className = \"Store\", uniqueKey = %s, enableSql = true),\n", cq("tw-"+workload+"-__tw_store"))
}

func storeBinding(name string, k StoreKindGen, b bundle.StoreBinding) string {
	if k.Binding == "wrapped" {
		return fmt.Sprintf("    (name = %s, wrapped = (moduleName = %s, innerBindings = [(name = \"fetcher\", service = (name = \"platform\", entrypoint = %s, props = (json = %s)))])),\n",
			cq(name), cq(k.WrappedModule), cq(k.Backend), cq(storeProps(b)))
	}
	return fmt.Sprintf("    (name = %s, %s = (name = \"platform\", entrypoint = %s, props = (json = %s))),\n",
		cq(name), k.Binding, cq(k.Backend), cq(storeProps(b)))
}

// storePlatformBindings are the platform worker's native bindings that
// facet proxies forward to.
func storePlatformBindings(app *bundle.App) string {
	var s strings.Builder
	for _, b := range app.Stores {
		if k, _ := storeGen(b.Kind); k.FacetProxy != "" {
			s.WriteString(storeBinding("__TW_STORE_"+b.Binding, k, b))
		}
	}
	return s.String()
}

// storeTenantBindings are the tenant worker's env.<Binding> entries.
func storeTenantBindings(app *bundle.App) string {
	var s strings.Builder
	for _, b := range app.Stores {
		k, _ := storeGen(b.Kind)
		s.WriteString(storeBinding(b.Binding, k, b))
	}
	return s.String()
}

// storeShimModule is the facet module name of kind's FacetShim.
func storeShimModule(kind string) string { return "__tw_store_" + kind + ".js" }

// storeFacetShims are the facet modules of app's kinds with a FacetShim
// (module name → source).
func storeFacetShims(app *bundle.App) map[string]string {
	out := map[string]string{}
	for _, k := range storeKinds(app) {
		if k.FacetShim != "" {
			out[storeShimModule(k.Kind)] = k.FacetShim
		}
	}
	return out
}

// storeFacetPrelude is __tw_facet.js's code that gives facet classes'
// env the shims (storeFacetClass); empty when no binding needs one. The
// env stays the object the runtime built (a Proxy over it), so its other
// bindings behave as before.
func storeFacetPrelude(app *bundle.App) string {
	shims := map[string]string{} // binding → import alias
	var b strings.Builder
	for _, k := range storeKinds(app) {
		if k.FacetShim != "" {
			fmt.Fprintf(&b, "import { wrap as __tw_wrap_%s } from %s;\n", k.Kind, jsq("./"+storeShimModule(k.Kind)))
		}
	}
	for _, sb := range app.Stores {
		if k, _ := storeGen(sb.Kind); k.FacetShim != "" {
			shims[sb.Binding] = "__tw_wrap_" + k.Kind
		}
	}
	if len(shims) == 0 {
		return ""
	}
	names := make([]string, 0, len(shims))
	for n := range shims {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("const __twShims = {\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s: %s,\n", jsq(n), shims[n])
	}
	b.WriteString(`};
const __twEnvs = new WeakMap();
function __twStoreEnv(env) {
  if (!env || typeof env !== "object") return env;
  let e = __twEnvs.get(env);
  if (!e) {
    const done = new Map();
    e = new Proxy(env, {
      get(t, k, r) {
        if (typeof k === "string" && Object.hasOwn(__twShims, k)) {
          if (!done.has(k)) done.set(k, __twShims[k](Reflect.get(t, k)));
          return done.get(k);
        }
        return Reflect.get(t, k, r);
      },
    });
    __twEnvs.set(env, e);
  }
  return e;
}
function __twStoreClass(C) {
  if (typeof C !== "function") return C;
  const W = class extends C {
    constructor(ctx, env) {
      super(ctx, __twStoreEnv(env));
    }
  };
  Object.defineProperty(W, "name", { value: C.name });
  return W;
}
`)
	return b.String()
}

// storeFacetClass wraps a facet class expression (expr) for the shims.
func storeFacetClass(app *bundle.App, expr string) string {
	for _, sb := range app.Stores {
		if k, _ := storeGen(sb.Kind); k.FacetShim != "" {
			return "__twStoreClass(" + expr + ")"
		}
	}
	return expr
}
