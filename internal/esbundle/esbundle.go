// Package esbundle bundles a tailworks app (wrangler.jsonc "main") into one ESM
// module, in-process via esbuild's Go API. It is the Go twin of
// js/packages/create-tailworks/lib/build.mjs (same options, same plugin), so a
// release install can `tw deploy` with no Node.js and no esbuild binary:
// workerd conditions, Node builtins mapped to node:* (needs nodejs_compat),
// cloudflare:* and node:* external, cloudflare:email stubbed for
// dependencies and refused for the app's own code (email.go). Beyond
// build.mjs it honours wrangler's build settings (define, alias, rules:
// rules.go), so a wrangler.toml project bundles as wrangler would.
package esbundle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/rosskukulinski/tailworks/internal/emailstub"
)

// Version is the esbuild version linked in (matches the npm pin).
const Version = "0.28.2"

// nodeBuiltins is Node's require("module").builtinModules (Node 25),
// without the node:-only ones (they are already node:*).
var nodeBuiltins = map[string]bool{}

func init() {
	for _, m := range strings.Fields(`_http_agent _http_client _http_common _http_incoming _http_outgoing _http_server
		_stream_duplex _stream_passthrough _stream_readable _stream_transform _stream_wrap _stream_writable
		_tls_common _tls_wrap assert assert/strict async_hooks buffer child_process cluster console constants
		crypto dgram diagnostics_channel dns dns/promises domain events fs fs/promises http http2 https
		inspector inspector/promises module net os path path/posix path/win32 perf_hooks process punycode
		querystring readline readline/promises repl stream stream/consumers stream/promises stream/web
		string_decoder sys timers timers/promises tls trace_events tty url util util/types v8 vm wasi
		worker_threads zlib`) {
		nodeBuiltins[m] = true
	}
}

// Options for Build.
type Options struct {
	Dir       string   // the app directory (absWorkingDir)
	Main      string   // the entry, relative to Dir (wrangler.jsonc "main"), or absolute
	Outfile   string   // absolute path of the output module
	NodePaths []string // module fallbacks (tailworks' own node_modules)
	Minify    bool

	// Wrangler build settings the built-in bundler honours (wrangler
	// config "define", "alias", "rules", "keep_names", "jsx_factory",
	// "jsx_fragment", "tsconfig"). Rules are applied after
	// EffectiveRules, so wrangler's defaults (.txt/.html/.sql text, .bin
	// data, .wasm modules) hold with no rules set.
	Define      map[string]string
	Alias       map[string]string
	Rules       []Rule
	KeepNames   bool
	JSXFactory  string
	JSXFragment string
	Tsconfig    string

	// SourceMap writes Outfile+".map" (external: no sourceMappingURL
	// comment); SourcesContent puts the sources in it too (for a debugger;
	// stack mapping needs only the positions).
	SourceMap      bool
	SourcesContent bool

	// NodeCompat: the app has nodejs_compat (NodeCompat(flags)), so a
	// require() of a Node builtin imports it (requirebuiltins.go).
	NodeCompat bool
}

// Result is what BuildApp wrote.
type Result struct {
	Size int // bytes of the entry module
	// Modules are the additional modules (wrangler "rules" matches) to
	// ship next to the entry, by name relative to it.
	Modules map[string]Module
}

var bareRE = regexp.MustCompile(`^[a-z_][a-z0-9_/]*$`)

// Build bundles and writes Outfile; it returns the output size.
func Build(o Options) (int, error) {
	r, err := BuildApp(o)
	if err != nil {
		return 0, err
	}
	return r.Size, nil
}

// BuildApp bundles, writes Outfile and returns it with the additional
// modules (not written: the caller packs them next to Outfile).
func BuildApp(o Options) (*Result, error) {
	if o.Main == "" {
		return nil, errors.New("wrangler config has no main")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	entry := o.Main
	if !filepath.IsAbs(entry) {
		entry = filepath.Join(dir, entry) // a generated entry (Pages functions) is absolute
	}
	res := &Result{Modules: map[string]Module{}}
	var mu sync.Mutex
	rules, err := rulesPlugin(EffectiveRules(o.Rules), res.Modules, &mu)
	if err != nil {
		return nil, err
	}
	email := &emailImports{dir: dir} // email.go
	tw := api.Plugin{Name: "tw", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^cloudflare:email$`}, email.resolve)
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "tw-stub"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			s := emailstub.ClassJS
			return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `^[a-z_][a-z0-9_/]*$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if !bareRE.MatchString(a.Path) {
				return api.OnResolveResult{}, nil
			}
			base, _, _ := strings.Cut(a.Path, "/")
			if nodeBuiltins[base] || nodeBuiltins[a.Path] {
				return api.OnResolveResult{Path: "node:" + a.Path, External: true}, nil
			}
			return api.OnResolveResult{}, nil
		})
	}}
	r := api.Build(api.BuildOptions{
		EntryPoints:       []string{entry},
		Outfile:           o.Outfile,
		AbsWorkingDir:     dir,
		Bundle:            true,
		Write:             true,
		Format:            api.FormatESModule,
		Platform:          api.PlatformNeutral,
		Target:            api.ES2022,
		Conditions:        []string{"workerd", "worker", "browser"},
		MainFields:        []string{"module", "main"},
		External:          []string{"cloudflare:*", "node:*"},
		MinifyWhitespace:  o.Minify,
		MinifyIdentifiers: o.Minify,
		MinifySyntax:      o.Minify,
		NodePaths:         o.NodePaths,
		LegalComments:     api.LegalCommentsNone,
		LogLevel:          api.LogLevelSilent,
		Plugins:           []api.Plugin{requireBuiltinsPlugin(dir, o.NodeCompat), tw, rules},
		Define:            o.Define,
		Alias:             o.Alias,
		KeepNames:         o.KeepNames,
		JSXFactory:        o.JSXFactory,
		JSXFragment:       o.JSXFragment,
		Tsconfig:          o.Tsconfig,
		Sourcemap:         sourceMapMode(o),
		SourcesContent:    sourcesContentMode(o),
	})
	if err := email.err(); err != nil {
		return nil, err
	}
	if len(r.Errors) > 0 {
		msgs := api.FormatMessages(r.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		return nil, fmt.Errorf("esbuild: %d error(s)\n%s", len(r.Errors), strings.TrimSpace(strings.Join(msgs, "")))
	}
	st, err := os.Stat(o.Outfile)
	if err != nil {
		return nil, err
	}
	res.Size = int(st.Size())
	return res, nil
}

func sourceMapMode(o Options) api.SourceMap {
	if o.SourceMap {
		return api.SourceMapExternal
	}
	return api.SourceMapNone
}

func sourcesContentMode(o Options) api.SourcesContent {
	if o.SourcesContent {
		return api.SourcesContentInclude
	}
	return api.SourcesContentExclude
}
