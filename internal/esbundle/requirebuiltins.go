package esbundle

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// require() of a Node builtin in a CommonJS dependency (issue #356: dotenv's
// `require("fs")`). The bundle is an ES module, and workerd's ES modules have
// no `require`: esbuild's __require shim throws `Dynamic require of "node:fs"
// is not supported` at start-up. As wrangler does with nodejs_compat, each
// such require resolves to a small CommonJS module that imports the builtin
// and exports it (its default export, which is what Node's require returns).
//
// Without nodejs_compat the node:* modules don't exist in workerd, so a
// static import would stop the Worker linking even when the require is
// guarded (`try { require("crypto") } catch {}`) and never runs. The require
// resolves to a module that throws, when it runs, an error naming the
// package and the flag. node:async_hooks always exists (tailworks runs every
// tenant with nodejs_als: hostd/gen/observe.go).
//
// build.mjs carries the same plugin (requireBuiltinsJS there).

// requireRE matches a bare or node: specifier (a builtin is checked in the callback).
const requireRE = `^(node:)?[a-z_][a-z0-9_/]*$`

var requireBuiltinRE = regexp.MustCompile(requireRE)

const (
	requireNS     = "tw-require"
	requireStubNS = "tw-require-stub"
)

// NodeCompat reports whether wrangler compatibility_flags turn on Node.js
// compatibility (the node:* modules).
func NodeCompat(flags []string) bool {
	for _, f := range flags {
		if f == "nodejs_compat" || f == "nodejs_compat_v2" {
			return true
		}
	}
	return false
}

// builtinName is the module name (without node:) when spec names a Node
// builtin, else "".
func builtinName(spec string) string {
	if !requireBuiltinRE.MatchString(spec) {
		return ""
	}
	if name, ok := strings.CutPrefix(spec, "node:"); ok {
		return name
	}
	base, _, _ := strings.Cut(spec, "/")
	if nodeBuiltins[base] || nodeBuiltins[spec] {
		return spec
	}
	return ""
}

// importerPackage names the package a file belongs to (its innermost
// node_modules directory), or the file relative to dir.
func importerPackage(dir, importer string) string {
	p := filepath.ToSlash(importer)
	if i := strings.LastIndex(p, "/node_modules/"); i >= 0 {
		parts := strings.SplitN(p[i+len("/node_modules/"):], "/", 3)
		if strings.HasPrefix(parts[0], "@") && len(parts) > 1 {
			return parts[0] + "/" + parts[1]
		}
		return parts[0]
	}
	if r, err := filepath.Rel(dir, importer); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

// requireBuiltinsPlugin must come before the plugin that marks builtins
// external: the first onResolve result wins.
func requireBuiltinsPlugin(dir string, nodeCompat bool) api.Plugin {
	return api.Plugin{Name: "tw-require", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: requireRE}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.Kind != api.ResolveJSRequireCall {
				return api.OnResolveResult{}, nil
			}
			name := builtinName(a.Path)
			if name == "" {
				return api.OnResolveResult{}, nil
			}
			if nodeCompat || name == "async_hooks" {
				return api.OnResolveResult{Path: "node:" + name, Namespace: requireNS}, nil
			}
			// One stub per package, so each names its own.
			return api.OnResolveResult{Path: "node:" + name + " in " + importerPackage(dir, a.Importer), Namespace: requireStubNS}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: requireNS}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			s := requireModule(a.Path)
			return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: requireStubNS}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			spec, pkg, _ := strings.Cut(a.Path, " in ")
			s := requireStub(spec, pkg)
			return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
		})
	}}
}

// requireModule is what require(spec) returns: the builtin's default export
// (Node's require value), or its namespace if it has none.
func requireModule(spec string) string {
	q := strconv.Quote(spec)
	return "import * as m from " + q + ";\nmodule.exports = \"default\" in m ? m.default : m;\n"
}

// requireStub throws, when required, the error for a builtin without nodejs_compat.
func requireStub(spec, pkg string) string {
	msg := `require("` + strings.TrimPrefix(spec, "node:") + `") in ` + pkg + ` needs Node.js compatibility: add "nodejs_compat" to compatibility_flags in the wrangler config`
	return "throw new Error(" + strconv.Quote(msg) + ");\n"
}
