package main

// Cloudflare Pages projects (ADR-051, issue #285): a static
// output directory, plus functions/ or an advanced-mode _worker.js. tw
// translates one onto what ADR-050 already serves: the output directory
// becomes "assets", functions/ is compiled (internal/pages) into the
// Worker's main module, and the function routes (or _routes.json) become
// run_worker_first patterns. docs/wrangler.md#pages.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/pages"
)

// pagesAssetsBinding is the binding Pages gives Functions (env.ASSETS).
const pagesAssetsBinding = "ASSETS"

// pagesProject is what loadWrangler found of a Pages project.
type pagesProject struct {
	OutDir     string            // the build output directory (absolute): assets.directory
	Functions  string            // functions/ (absolute; "" for none, or when _worker.js wins)
	Worker     string            // the advanced-mode module (absolute): _worker.js or _worker.js/index.js
	Routes     []pages.Route     // functions/ routes
	RoutesJSON *pages.RoutesJSON // <output dir>/_routes.json, nil for none
	Binding    string            // the assets binding the Worker falls through to
	Err        error             // a bad functions/ or _routes.json: the build fails with it
}

// hasCode: the project has a Worker (functions with handlers, or _worker.js).
func (p *pagesProject) hasCode() bool {
	return p != nil && (p.Worker != "" || len(p.Routes) > 0)
}

// detectPages recognises a Pages project and rewrites a.Wrangler to the
// Workers-with-assets config it amounts to. A project is Pages when its
// config has pages_build_output_dir, or when it has no main and has
// functions/ or <assets.directory>/_worker.js. A config with main is a
// Worker; its functions/ (if any) is its own business.
func (a *app) detectPages() error {
	w := a.Wrangler
	file := a.WranglerFile
	out, _ := w["pages_build_output_dir"].(string)
	am, hasAssets := w["assets"].(map[string]any)
	pagesCfg := out != ""
	if pagesCfg {
		if wranglerMain(w) != "" {
			return fail("bad_request", file+": pages_build_output_dir and main: a Pages project has no main").
				withFix("drop main (Pages compiles functions/ or <output dir>/_worker.js into the Worker)", "or drop pages_build_output_dir and use \"assets\" (a Worker with static assets)")
		}
		if _, ok := w["assets"]; ok {
			return fail("bad_request", file+": pages_build_output_dir and assets: pick one").
				withFix("drop assets: pages_build_output_dir is the assets directory of a Pages project")
		}
	} else {
		if wranglerMain(w) != "" {
			return nil
		}
		if hasAssets {
			out, _ = am["directory"].(string)
		}
		fn := dirExists(filepath.Join(a.Dir, "functions"))
		wk := false
		if out != "" {
			_, err := os.Stat(filepath.Join(a.absDir(out), "_worker.js"))
			wk = err == nil
		}
		if !fn && !wk {
			return nil
		}
		if out == "" {
			return fail("bad_request", file+": functions/ looks like a Cloudflare Pages project, but there is no output directory").
				withFix(`add "pages_build_output_dir": "./public" (the folder of static files your build writes) to ` + file)
		}
	}
	p := &pagesProject{OutDir: a.absDir(out)}
	if !dirExists(p.OutDir) {
		return fail("bad_request", fmt.Sprintf("%s: the Pages output directory %s is not a directory", file, rel(a.Dir, p.OutDir))).
			withFix("build the site first (for example `npm run build`), or fix pages_build_output_dir")
	}
	if st, err := os.Stat(filepath.Join(p.OutDir, "_worker.js")); err == nil {
		p.Worker = filepath.Join(p.OutDir, "_worker.js")
		if st.IsDir() {
			p.Worker = filepath.Join(p.Worker, "index.js")
			if _, err := os.Stat(p.Worker); err != nil {
				p.Err = fail("bad_request", rel(a.Dir, p.OutDir)+"/_worker.js/ has no index.js").withFix("an advanced-mode _worker.js directory needs index.js as its entry")
			}
		}
	}
	if fd := filepath.Join(a.Dir, "functions"); dirExists(fd) {
		if p.Worker != "" {
			a.Warnings = append(a.Warnings, fmt.Sprintf("functions/: ignored: %s/_worker.js is the Worker (advanced mode), as on Cloudflare", rel(a.Dir, p.OutDir)))
		} else {
			p.Functions = fd
			routes, err := pages.Discover(fd)
			if err != nil {
				p.Err = fail("bad_request", err.Error())
			}
			p.Routes = routes
		}
	}
	if b, err := os.ReadFile(filepath.Join(p.OutDir, "_routes.json")); err == nil {
		rj, err := pages.ParseRoutesJSON(b)
		if err != nil && p.Err == nil {
			p.Err = fail("bad_request", rel(a.Dir, p.OutDir)+"/"+err.Error())
		}
		p.RoutesJSON = rj
	}

	// The assets config: the user's ("assets" with no main), or Pages'
	// defaults for pages_build_output_dir.
	nm := map[string]any{}
	for k, v := range am {
		nm[k] = v
	}
	if pagesCfg {
		nm["directory"] = out
		if _, ok := nm["not_found_handling"]; !ok {
			// Pages: a site with no 404.html anywhere is a single-page app.
			nm["not_found_handling"] = "single-page-application"
			if has404(p.OutDir) {
				nm["not_found_handling"] = "404-page"
			}
		}
	}
	if b, _ := nm["binding"].(string); b != "" {
		p.Binding = b
	} else {
		p.Binding = pagesAssetsBinding
		nm["binding"] = pagesAssetsBinding
	}
	if _, set := nm["run_worker_first"]; !set && p.hasCode() {
		switch {
		case p.RoutesJSON != nil:
			nm["run_worker_first"] = p.RoutesJSON.Patterns()
		case p.Worker != "":
			nm["run_worker_first"] = true
		default:
			nm["run_worker_first"] = pages.Patterns(p.Routes)
		}
	}
	w["assets"] = nm
	a.Pages = p
	what := "static files only"
	switch {
	case p.Worker != "":
		what = rel(a.Dir, p.OutDir) + "/_worker.js is the Worker"
	case len(p.Routes) > 0:
		what = fmt.Sprintf("functions/ (%d file(s)) compiled into the Worker", len(p.Routes))
	}
	a.Warnings = append(a.Warnings, fmt.Sprintf("Cloudflare Pages project: %s served as assets; %s (docs/wrangler.md#pages)", rel(a.Dir, p.OutDir), what))
	return nil
}

// absDir resolves a config path against the app directory.
func (a *app) absDir(d string) string {
	if filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(a.Dir, d)
}

// has404 reports a 404.html anywhere in dir.
func has404(dir string) bool {
	found := false
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipAll
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "404.html" {
			found = true
		}
		return nil
	})
	return found
}

// pagesSkipAsset: _worker.js and _routes.json configure a Pages project;
// they are never served.
func (a *app) pagesSkipAsset(r string) bool {
	return a.Pages != nil && (r == "_worker.js" || r == "_routes.json")
}

// pagesEntry writes the Worker's entry module into tmp and returns its
// path: the generated Functions router, the _worker.js itself, or the
// _worker.js behind its _routes.json.
func (a *app) pagesEntry(tmp string) (string, error) {
	p := a.Pages
	if p.Err != nil {
		return "", p.Err
	}
	if a.Opts.builder() == "wrangler" {
		return "", fail("bad_request", "--wrangler and --from-wrangler-out don't build Cloudflare Pages projects (wrangler's is `wrangler pages functions build`)").
			withFix("drop the flag: tw compiles functions/ itself")
	}
	var src []byte
	switch {
	case p.Worker != "" && p.RoutesJSON == nil:
		return p.Worker, nil
	case p.Worker != "":
		src = pages.AdvancedEntry(p.Worker, p.Binding, p.RoutesJSON)
	default:
		src = pages.FunctionsEntry(p.Functions, p.Routes, p.Binding, p.RoutesJSON)
	}
	f := filepath.Join(tmp, "__tw_pages_entry.js")
	return f, os.WriteFile(f, src, 0o644)
}

// pagesEnvWarnings: names functions/ reads from env that nothing
// declares (on Cloudflare they are often set in the Pages dashboard,
// which tw can't read). declared are the names tw types would emit.
func (a *app) pagesEnvWarnings(declared []string) {
	p := a.Pages
	if p == nil || p.Functions == "" || len(p.Routes) == 0 {
		return
	}
	have := map[string]bool{}
	for _, n := range declared {
		have[n] = true
	}
	for _, v := range a.Wrangler {
		for _, n := range bindingNames(v) {
			have[n] = true // binding sections tw has no binding for warn on their own
		}
	}
	where := map[string]string{}
	filepath.WalkDir(p.Functions, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if fp != p.Functions && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(fp) {
		case ".js", ".mjs", ".ts", ".jsx", ".tsx":
		default:
			return nil
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return nil
		}
		r, _ := filepath.Rel(p.Functions, fp)
		for _, n := range pages.EnvNames(b) {
			if !have[n] && where[n] == "" {
				where[n] = filepath.ToSlash(r)
			}
		}
		return nil
	})
	if len(where) == 0 {
		return
	}
	names := make([]string, 0, len(where))
	for n := range where {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("env.%s (%s)", n, where[n]))
	}
	a.Warnings = append(a.Warnings, fmt.Sprintf("functions/ reads %s, but %s doesn't declare it: it will be undefined. Add the binding (vars, d1_databases, kv_namespaces, …) to %s, or for a secret run `tw secret put <NAME>` and list it in secrets.required (Pages dashboard bindings aren't in git)",
		strings.Join(parts, ", "), a.WranglerFile, a.WranglerFile))
}
