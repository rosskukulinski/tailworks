package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

const pagesHandler = `export const onRequestGet = ({ env }) => env.DB.prepare("x").all().then((r) => Response.json(r));`

// buildPages loads and builds dir and parses the bundle as hostd would.
func buildPages(t *testing.T, dir string) (*app, *bundle.App) {
	t.Helper()
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	tb, _, err := a.build(nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := bundle.Unpack(tb)
	if err != nil {
		t.Fatal(err)
	}
	app, err := bundle.Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	return a, app
}

// TestPagesFunctionsBuild: pages_build_output_dir becomes assets (binding
// ASSETS, 404-page when there is a 404.html), the function routes become
// run_worker_first, functions/ compiles into main, and _routes.json isn't
// served.
func TestPagesFunctionsBuild(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":                `{"name":"arcade","pages_build_output_dir":"./dist","d1_databases":[{"binding":"DB","database_name":"a","database_id":"x"}]}`,
		"dist/index.html":               "<h1>arcade</h1>",
		"dist/404.html":                 "lost",
		"functions/api/scores/index.js": pagesHandler,
		"functions/api/scores/[id].js":  `export const onRequestDelete = ({ params }) => new Response(params.id);`,
		"functions/_middleware.ts":      `export const onRequest = ({ next }) => next();`,
	})
	a, app := buildPages(t, dir)
	if a.Pages == nil || len(a.Pages.Routes) != 3 {
		t.Fatalf("pages %+v", a.Pages)
	}
	as := app.Assets
	if as == nil || as.Only || as.Binding != "ASSETS" || as.NotFound != bundle.NotFound404 {
		t.Fatalf("assets %+v", as)
	}
	if !reflect.DeepEqual(as.WorkerFirst.Patterns, []string{"/*"}) {
		t.Errorf("run_worker_first %+v (a root middleware covers every path)", as.WorkerFirst)
	}
	if _, ok := as.Files["/index.html"]; !ok {
		t.Errorf("files %v", as.AssetPaths())
	}
	main := string(app.Modules[app.Main])
	for _, want := range []string{"__twPagesFunctions", `routePath: "/api/scores/:id"`, "onRequestDelete"} {
		if !strings.Contains(main, want) {
			t.Errorf("main module lacks %s", want)
		}
	}
	for _, w := range a.Warnings {
		if strings.Contains(w, "reads env.") || strings.Contains(w, "unknown key") {
			t.Errorf("warning %q", w)
		}
	}
}

// TestPagesSPADefault: no 404.html anywhere: Pages serves the site as a
// single-page app. Without a middleware the patterns are the routes'.
func TestPagesSPADefault(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.toml":          "name = \"spa\"\npages_build_output_dir = \"public\"\n",
		"public/index.html":      "<div id=app>",
		"public/_routes.json":    `{"version":1,"include":["/api/*"],"exclude":[]}`,
		"functions/api/[id].js":  `export const onRequest = () => new Response("x");`,
		"functions/api/stats.js": `export const onRequest = () => new Response("x");`,
	})
	_, app := buildPages(t, dir)
	if app.Assets.NotFound != bundle.NotFoundSPA {
		t.Errorf("not_found_handling %q", app.Assets.NotFound)
	}
	if !reflect.DeepEqual(app.Assets.WorkerFirst.Patterns, []string{"/api/*"}) {
		t.Errorf("run_worker_first %+v (from _routes.json)", app.Assets.WorkerFirst)
	}
	if _, ok := app.Assets.Files["/_routes.json"]; ok {
		t.Error("_routes.json is served")
	}
}

// TestPagesAdvancedBuild: <output dir>/_worker.js (a file or a directory)
// is main, every request runs it first, and it isn't served. functions/
// is ignored with a warning, as on Cloudflare.
func TestPagesAdvancedBuild(t *testing.T) {
	for _, worker := range []string{"_worker.js", "_worker.js/index.js"} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"wrangler.jsonc":     `{"name":"adv","pages_build_output_dir":"out"}`,
			"out/index.html":     "hi",
			"out/" + worker:      `export default { fetch: (req, env) => env.ASSETS.fetch(req) };`,
			"functions/api/x.js": `export const onRequest = () => new Response("x");`,
		})
		a, app := buildPages(t, dir)
		if !app.Assets.WorkerFirst.All {
			t.Errorf("%s: run_worker_first %+v", worker, app.Assets.WorkerFirst)
		}
		for p := range app.Assets.Files {
			if strings.HasPrefix(p, "/_worker.js") {
				t.Errorf("%s: %s is served", worker, p)
			}
		}
		if !strings.Contains(string(app.Modules[app.Main]), "env.ASSETS.fetch") {
			t.Errorf("%s: main is not the _worker.js", worker)
		}
		if !strings.Contains(strings.Join(a.Warnings, "\n"), "functions/: ignored") {
			t.Errorf("%s: warnings %q", worker, a.Warnings)
		}
	}
}

// TestPagesDetection: a Worker (main) is never Pages; functions/ with no
// main but "assets" is; Pages-only and conflicting configs fail clearly.
func TestPagesDetection(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":     `{"name":"w","main":"src/index.js"}`,
		"src/index.js":       `export default { fetch: () => new Response("w") };`,
		"functions/api/x.js": `export const onRequest = () => new Response("x");`,
	})
	if a, err := loadApp(dir, ""); err != nil || a.Pages != nil {
		t.Errorf("a Worker with functions/: %v %+v", err, a)
	}

	dir = t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":     `{"name":"w","assets":{"directory":"./site","binding":"STATIC","not_found_handling":"none"}}`,
		"site/index.html":    "x",
		"functions/api/x.js": `export const onRequest = ({ env }) => env.STATIC.fetch("/");`,
	})
	_, app := buildPages(t, dir)
	if app.Assets.Binding != "STATIC" || app.Assets.NotFound != bundle.NotFoundNone || !reflect.DeepEqual(app.Assets.WorkerFirst.Patterns, []string{"/api/x"}) {
		t.Errorf("assets + functions/: %+v", app.Assets)
	}

	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"main and pages":   {map[string]string{"wrangler.jsonc": `{"name":"w","main":"a.js","pages_build_output_dir":"p"}`, "p/x": ""}, "pages_build_output_dir and main"},
		"assets and pages": {map[string]string{"wrangler.jsonc": `{"name":"w","assets":{"directory":"p"},"pages_build_output_dir":"p"}`, "p/x": ""}, "pick one"},
		"no output dir":    {map[string]string{"wrangler.jsonc": `{"name":"w"}`, "functions/a.js": "onRequest"}, "no output directory"},
		"no wrangler":      {map[string]string{"functions/a.js": "onRequest"}, "looks like a Cloudflare Pages project"},
		"missing dir":      {map[string]string{"wrangler.jsonc": `{"name":"w","pages_build_output_dir":"nope"}`}, "is not a directory"},
	} {
		dir := t.TempDir()
		writeFiles(t, dir, c.files)
		_, err := loadApp(dir, "")
		if err == nil || !strings.Contains(err.Error()+strings.Join(errFixes(err), " "), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func errFixes(err error) []string {
	if e, ok := err.(*cliErr); ok {
		return e.Fix
	}
	return nil
}

// TestPagesBuildErrors: a bad route name or _routes.json fails the build
// (not the load), and --wrangler is refused.
func TestPagesBuildErrors(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"catch-all not last": {"functions/[[a]]/b.js": `export const onRequest = 1;`},
		"bad _routes.json":   {"functions/a.js": `export const onRequest = 1;`, "p/_routes.json": `{"version":1,"include":[]}`},
	} {
		dir := t.TempDir()
		files["wrangler.jsonc"] = `{"name":"w","pages_build_output_dir":"p"}`
		files["p/index.html"] = "x"
		writeFiles(t, dir, files)
		a, err := loadApp(dir, "")
		if err != nil {
			t.Fatalf("%s: load: %v", name, err)
		}
		if _, _, err := a.build(nil); err == nil {
			t.Errorf("%s: built", name)
		}
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"wrangler.jsonc": `{"name":"w","pages_build_output_dir":"p"}`, "p/i.html": "x", "functions/a.js": `export const onRequest = () => {};`})
	a, err := loadAppWith(dir, "", appOpts{Wrangler: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.build(nil); err == nil || !strings.Contains(err.Error(), "don't build Cloudflare Pages projects") {
		t.Errorf("--wrangler: %v", err)
	}
}

// TestPagesEnvWarning: env names functions/ reads that nothing declares
// get one warning; declared bindings, vars, secrets and ASSETS don't.
func TestPagesEnvWarning(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc": `{"name":"w","pages_build_output_dir":"p","vars":{"MODE":"x"},
		  "kv_namespaces":[{"binding":"KV","id":"k"}],"secrets":{"required":["TOKEN"]}}`,
		".dev.vars":    "DEV_ONLY=1\n",
		"p/index.html": "x",
		"functions/api/a.js": `export async function onRequest({ env }) {
  const { KV, MISSING_ONE } = env;
  return Response.json([env.DB, env.MODE, env.TOKEN, env.DEV_ONLY, env.ASSETS, env.ADMIN_KEY, process.env.NODE_ENV]);
}`,
	})
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range a.Warnings {
		if strings.Contains(w, "functions/ reads") {
			got = append(got, w)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "env.ADMIN_KEY (api/a.js), env.DB (api/a.js), env.MISSING_ONE (api/a.js), but") {
		t.Errorf("warnings %q", got)
	}
}
