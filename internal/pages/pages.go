// Package pages compiles a Cloudflare Pages project's functions/ directory
// (and an advanced-mode _worker.js) into one Worker module that runs in
// front of the project's static assets (ADR-051). tw deploy
// and tw dev bundle the module it generates with the built-in esbuild, so
// a deploy needs no wrangler.
//
// The routing follows wrangler's Pages compiler (filepath routing and the
// pages-template-worker runtime): [id] is a parameter, [[rest]] a
// catch-all, index the directory itself, _middleware a middleware for the
// directory and everything under it; onRequest and onRequest<Method>
// exports are the handlers; next() with no route left falls through to
// env.ASSETS.
package pages

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed runtime.js
var runtimeJS string

// MaxRules is Cloudflare's limit on _routes.json include + exclude rules.
// A generated list longer than this becomes "/*", as wrangler does.
const MaxRules = 100

// Route is one functions/ file and where it routes.
type Route struct {
	// File is the path relative to the functions directory, with slashes.
	File string
	// RoutePath is wrangler's route: "/api/:id", "/files/:path*", "/".
	RoutePath string
	// MountPath is the route of the file's directory (context.functionPath
	// of a middleware is the part of the URL it matched).
	MountPath string
	// Middleware is a _middleware file: it runs for its directory and
	// everything under it, before the matching route.
	Middleware bool
}

// sourceExts are the files wrangler's filepath routing compiles.
var sourceExts = map[string]bool{".js": true, ".mjs": true, ".ts": true, ".jsx": true, ".tsx": true}

// handlerRE finds a handler export by name. A file that never says
// onRequest has no handlers: it's a helper another file imports, not a
// route.
var handlerRE = regexp.MustCompile(`\bonRequest(?:Get|Post|Put|Patch|Delete|Head|Options)?\b`)

var identRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// Discover walks a functions directory and returns its routes, sorted by
// file. node_modules and dot directories are skipped, as are .d.ts files
// and files that define no handler.
func Discover(dir string) ([]Route, error) {
	var out []Route
	err := filepath.WalkDir(dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if fp != dir && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(dir, fp)
		r = filepath.ToSlash(r)
		ext := path.Ext(r)
		if !sourceExts[ext] || strings.HasSuffix(r, ".d.ts") {
			return nil
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		if !handlerRE.Match(b) {
			return nil
		}
		rt, err := routeFor(r)
		if err != nil {
			return err
		}
		out = append(out, rt)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

// routeFor maps a functions-relative file to its route.
func routeFor(file string) (Route, error) {
	parts := strings.Split(strings.TrimSuffix(file, path.Ext(file)), "/")
	base := parts[len(parts)-1]
	var dirSegs []string
	for _, p := range parts[:len(parts)-1] {
		s, err := segment(file, p)
		if err != nil {
			return Route{}, err
		}
		dirSegs = append(dirSegs, s)
	}
	mount := "/" + strings.Join(dirSegs, "/")
	rt := Route{File: file, MountPath: mount, RoutePath: mount}
	switch base {
	case "_middleware":
		rt.Middleware = true
	case "index":
	default:
		s, err := segment(file, base)
		if err != nil {
			return Route{}, err
		}
		rt.RoutePath = path.Join(mount, s)
	}
	segs := strings.Split(strings.Trim(rt.RoutePath, "/"), "/")
	for i, s := range segs {
		if strings.HasSuffix(s, "*") && i != len(segs)-1 {
			return Route{}, fmt.Errorf("functions/%s: a [[catch-all]] must be the last part of the path", file)
		}
	}
	if rt.Middleware && strings.HasSuffix(rt.RoutePath, "*") {
		return Route{}, fmt.Errorf("functions/%s: a _middleware can't be under a [[catch-all]] directory", file)
	}
	return rt, nil
}

// segment converts one path part: [[x]] -> :x*, [x] -> :x, else itself.
func segment(file, p string) (string, error) {
	name, star := "", false
	switch {
	case strings.HasPrefix(p, "[[") && strings.HasSuffix(p, "]]"):
		name, star = p[2:len(p)-2], true
	case strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]"):
		name = p[1 : len(p)-1]
	case strings.ContainsAny(p, "[]:*"):
		return "", fmt.Errorf("functions/%s: %q: a parameter is a whole path part, like [id] or [[path]]", file, p)
	default:
		return p, nil
	}
	if !identRE.MatchString(name) {
		return "", fmt.Errorf("functions/%s: parameter %q: want a JavaScript identifier", file, name)
	}
	if star {
		return ":" + name + "*", nil
	}
	return ":" + name, nil
}

var globRE = regexp.MustCompile(`:\w+\*?.*`)

// Patterns are the paths the routes answer, as run_worker_first patterns
// (`*` matches any characters): wrangler's generated _routes.json
// include list. Everything from the first parameter on becomes `*`; a
// middleware covers its directory and everything under it. Patterns a
// wildcard already covers are dropped; more than MaxRules become "/*".
func Patterns(routes []Route) []string {
	set := map[string]bool{}
	for _, r := range routes {
		g := globRE.ReplaceAllString(r.RoutePath, "*")
		if r.Middleware && !strings.HasSuffix(g, "*") {
			base := strings.TrimSuffix(g, "/")
			set[base+"/*"] = true
			if base != "" {
				set[base] = true // Cloudflare's /dir/* also answers /dir
			}
			continue
		}
		set[g] = true
	}
	var out []string
	for p := range set {
		covered := false
		for q := range set {
			if q != p && strings.HasSuffix(q, "/*") && strings.HasPrefix(p, strings.TrimSuffix(q, "*")) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) > MaxRules {
		return []string{"/*"}
	}
	return out
}

// RoutesJSON is a project's _routes.json: which paths run the Functions
// (or _worker.js). Exclude wins over include.
type RoutesJSON struct {
	Version int      `json:"version"`
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// ParseRoutesJSON reads and checks a _routes.json.
func ParseRoutesJSON(b []byte) (*RoutesJSON, error) {
	var r RoutesJSON
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("_routes.json: %w", err)
	}
	if r.Version != 1 {
		return nil, fmt.Errorf("_routes.json: version %d: want 1", r.Version)
	}
	if len(r.Include) == 0 {
		return nil, fmt.Errorf("_routes.json: include is empty: list at least one path (\"/*\" for all)")
	}
	if n := len(r.Include) + len(r.Exclude); n > MaxRules {
		return nil, fmt.Errorf("_routes.json: %d rules: Cloudflare allows at most %d (include and exclude together)", n, MaxRules)
	}
	for _, p := range append(append([]string{}, r.Include...), r.Exclude...) {
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("_routes.json: rule %q must start with /", p)
		}
	}
	return &r, nil
}

// Patterns are the rules as run_worker_first patterns: include, then each
// exclude with "!".
func (r *RoutesJSON) Patterns() []string {
	out := append([]string{}, r.Include...)
	for _, p := range r.Exclude {
		out = append(out, "!"+p)
	}
	return out
}

// filterJS is the routes filter the runtime checks (null for none).
func filterJS(r *RoutesJSON) string {
	if r == nil {
		return "null"
	}
	b, _ := json.Marshal(map[string][]string{"include": nonNil(r.Include), "exclude": nonNil(r.Exclude)})
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// FunctionsEntry is the Worker module for a functions directory: it
// imports every route file (by absolute path, so it can live anywhere,
// such as a build's temp dir) and serves them with the Pages runtime.
// binding is the assets binding next() and unmatched paths fall through
// to; filter is the project's _routes.json (nil for none).
func FunctionsEntry(dir string, routes []Route, binding string, filter *RoutesJSON) []byte {
	var b strings.Builder
	b.WriteString("// Generated by tw from functions/ (Cloudflare Pages Functions).\n")
	for i, r := range routes {
		fmt.Fprintf(&b, "import * as __f%d from %s;\n", i, jsString(filepath.Join(dir, filepath.FromSlash(r.File))))
	}
	b.WriteString(runtimeJS)
	b.WriteString("const __twPagesRoutes = [\n")
	for i, r := range routes {
		fmt.Fprintf(&b, "  { file: %s, routePath: %s, mountPath: %s, middleware: %t, module: __f%d },\n",
			jsString(r.File), jsString(r.RoutePath), jsString(r.MountPath), r.Middleware, i)
	}
	b.WriteString("];\n")
	fmt.Fprintf(&b, "export default __twPagesFunctions(__twPagesRoutes, %s, %s);\n", jsString(binding), filterJS(filter))
	return []byte(b.String())
}

// AdvancedEntry wraps an advanced-mode _worker.js (module at abs path
// worker) so that paths _routes.json leaves out go to assets for every
// method (the asset front only sees GET and HEAD). Its other exports pass
// through.
func AdvancedEntry(worker, binding string, filter *RoutesJSON) []byte {
	var b strings.Builder
	b.WriteString("// Generated by tw: a Pages _worker.js behind its _routes.json.\n")
	fmt.Fprintf(&b, "import * as __w from %s;\nexport * from %s;\n", jsString(worker), jsString(worker))
	b.WriteString(runtimeJS)
	fmt.Fprintf(&b, "export default __twPagesAdvanced(__w.default, %s, %s);\n", jsString(binding), filterJS(filter))
	return []byte(b.String())
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// envRE finds env.NAME reads: context.env.X, env.X, env["X"]. The word
// before env is captured so process.env and import.meta.env are left out.
var envRE = regexp.MustCompile(`(\w+\.)?\benv(?:\.([A-Za-z_$][A-Za-z0-9_$]*)|\[\s*["']([A-Za-z_$][A-Za-z0-9_$]*)["']\s*\])`)

// envDestructRE finds `const { A, B } = env` (or context.env).
var envDestructRE = regexp.MustCompile(`\{([^{}]*)\}\s*=\s*(?:\w+\.)?env\b`)

// EnvNames scans JavaScript or TypeScript source for the env names it
// reads. It is a text scan, so it can miss computed names and see names
// in comments; it's for a warning, not a check.
func EnvNames(src []byte) []string {
	set := map[string]bool{}
	for _, m := range envRE.FindAllSubmatch(src, -1) {
		if p := string(m[1]); p == "process." || p == "meta." {
			continue
		}
		n := string(m[2])
		if n == "" {
			n = string(m[3])
		}
		set[n] = true
	}
	for _, m := range envDestructRE.FindAllSubmatch(src, -1) {
		for _, f := range strings.Split(string(m[1]), ",") {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "...") {
				continue // the rest of env, not a name
			}
			f, _, _ = strings.Cut(f, ":")
			f, _, _ = strings.Cut(f, "=")
			f = strings.TrimSpace(f)
			if identRE.MatchString(f) {
				set[f] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
