package gen_test

// Cloudflare Pages projects (ADR-051): functions/ compiled by
// internal/pages, bundled by internal/esbundle (as tw deploy does), run in
// real workerd behind the ADR-050 assets router.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
	"github.com/rosskukulinski/tailworks/internal/pages"
)

// pagesFunctions is a small Pages site's functions/ directory.
var pagesFunctions = map[string]string{
	// Root middleware: runs first for every function route; data flows on.
	"_middleware.js": `export async function onRequest(ctx) {
  ctx.data.trace = ["root"];
  const r = await ctx.next();
  r.headers.set("x-trace", ctx.data.trace.join(","));
  return r;
}`,
	// A chain: export const onRequest = [a, b].
	"api/_middleware.js": `export const onRequest = [
  async (ctx) => { ctx.data.trace.push("api-a"); return ctx.next(); },
  async (ctx) => { ctx.data.trace.push("api-b:" + ctx.functionPath); return ctx.next(); },
];`,
	"index.js":     `export const onRequestPost = () => new Response("posted home");`,
	"api/index.js": `export const onRequest = ({ request }) => Response.json({ route: "api", method: request.method });`,
	"api/scores.js": `export async function onRequestGet({ env, data }) {
  return Response.json({ route: "scores", trace: data.trace, asset: typeof env.ASSETS?.fetch });
}
export async function onRequestPost({ request }) {
  return Response.json({ route: "scores-post", body: await request.text() }, { status: 201 });
}`,
	"api/scores/[id].js": `export const onRequestDelete = ({ params, functionPath }) => Response.json({ route: "score", id: params.id, functionPath });
export const onRequestGet = ({ params }) => Response.json({ route: "score-get", id: params.id });`,
	"api/scores/top.js":         `export const onRequestGet = () => Response.json({ route: "top" });`,
	"api/users/[user]/posts.js": `export const onRequest = ({ params }) => Response.json({ route: "posts", user: params.user });`,
	"files/[[path]].js": `export const onRequest = ({ params, next }) =>
  params.path?.[0] === "public" ? next() : Response.json({ route: "files", path: params.path ?? null });`,
	// next(input): fetch a different asset.
	"rewrite.js":    `export const onRequestGet = ({ next }) => next("/about.html");`,
	"boom.js":       `export const onRequest = ({ passThroughOnException }) => { passThroughOnException(); throw new Error("x"); };`,
	"lib/helper.js": `export const helper = 1; // not a route`,
}

// startPages builds functions (nil: none) or an advanced-mode worker
// ("" for none) the way tw does, with rj as _routes.json ("" for none).
func startPages(t *testing.T, functions map[string]string, worker, rj string) *assetClient {
	t.Helper()
	tmp := t.TempDir()
	var filter *pages.RoutesJSON
	if rj != "" {
		var err error
		if filter, err = pages.ParseRoutesJSON([]byte(rj)); err != nil {
			t.Fatal(err)
		}
	}
	assets := map[string]any{"directory": "public", "binding": "ASSETS", "not_found_handling": "404-page"}
	var entry []byte
	switch {
	case worker != "":
		wp := filepath.Join(tmp, "_worker.js")
		os.WriteFile(wp, []byte(worker), 0o644)
		entry = pages.AdvancedEntry(wp, "ASSETS", filter)
		assets["run_worker_first"] = true
	default:
		fd := filepath.Join(tmp, "functions")
		for p, s := range functions {
			fp := filepath.Join(fd, filepath.FromSlash(p))
			os.MkdirAll(filepath.Dir(fp), 0o755)
			os.WriteFile(fp, []byte(s), 0o644)
		}
		routes, err := pages.Discover(fd)
		if err != nil {
			t.Fatal(err)
		}
		entry = pages.FunctionsEntry(fd, routes, "ASSETS", filter)
		assets["run_worker_first"] = pages.Patterns(routes)
	}
	if filter != nil {
		assets["run_worker_first"] = filter.Patterns()
	}
	ep := filepath.Join(tmp, "entry.js")
	os.WriteFile(ep, entry, 0o644)
	out := filepath.Join(tmp, "out", "index.js")
	if _, err := esbundle.BuildApp(esbundle.Options{Dir: tmp, Main: ep, Outfile: out}); err != nil {
		t.Fatal(err)
	}
	js, _ := os.ReadFile(out)
	cfg, _ := json.Marshal(map[string]any{"name": "w", "compatibility_date": "2026-04-01", "main": "index.js", "assets": assets})
	files := map[string][]byte{"wrangler.jsonc": cfg, "index.js": js}
	for p, b := range assetFiles {
		files[bundle.AssetsPrefix+p] = []byte(b)
	}
	files[bundle.AssetsPrefix+"files/public/f.txt"] = []byte("public file")
	files[bundle.AssetsPrefix+"api/scores/top.txt"] = []byte("a file next to a function")
	return &assetClient{t: t, in: startFiles(t, files)}
}

// jsonField reads one field of a JSON body.
func jsonField(r assetResp, k string) any {
	var m map[string]any
	json.Unmarshal([]byte(r.body), &m)
	return m[k]
}

func (c *assetClient) route(method, path, want string) assetResp {
	c.t.Helper()
	r := c.do(method, path, "workload")
	if got, _ := jsonField(r, "route").(string); got != want {
		c.t.Errorf("%s %s: %d %q, want route %q", method, path, r.code, r.body, want)
	}
	return r
}

// TestPagesFunctionsRouting: static, [param], [[catchall]] and nested
// routes; per-method handlers; route order (a literal beats a param).
func TestPagesFunctionsRouting(t *testing.T) {
	c := startPages(t, pagesFunctions, "", "")
	c.route("GET", "/api", "api")
	c.route("GET", "/api/", "api")
	c.route("GET", "/API/Scores", "scores") // path-to-regexp is case-insensitive
	c.route("POST", "/api/scores", "scores-post")
	if r := c.route("DELETE", "/api/scores/42", "score"); jsonField(r, "id") != "42" || jsonField(r, "functionPath") != "/api/scores/42" {
		t.Errorf("DELETE: %s", r.body)
	}
	c.route("GET", "/api/scores/7", "score-get")
	c.route("GET", "/api/scores/top", "top") // more specific than [id]
	if r := c.route("PUT", "/api/users/ann/posts", "posts"); jsonField(r, "user") != "ann" {
		t.Errorf("nested param: %s", r.body)
	}
	if r := c.route("GET", "/files", "files"); jsonField(r, "path") != nil {
		t.Errorf("catch-all, no segments: %s", r.body)
	}
	if r := c.route("GET", "/files/a/b/c", "files"); strings.Join(toStrings(jsonField(r, "path")), "/") != "a/b/c" {
		t.Errorf("catch-all: %s", r.body)
	}
	// A method with no handler on the route falls through to assets.
	if r := c.do("PATCH", "/api/scores/1", "workload"); r.code != 405 {
		t.Errorf("PATCH with no handler: %d %q, want the assets binding's 405", r.code, r.body)
	}
	if r := c.do("POST", "/", "workload"); r.body != "posted home" {
		t.Errorf("POST /: %d %q", r.code, r.body)
	}
	// The helper isn't a route: its path is a miss.
	c.status("/lib/helper", 404, "root 404")
}

func toStrings(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			s, _ := x.(string)
			out = append(out, s)
		}
	}
	return out
}

// TestPagesFunctionsMiddleware: middleware at every level, chains, data
// shared across them, functionPath of a middleware is its mount.
func TestPagesFunctionsMiddleware(t *testing.T) {
	c := startPages(t, pagesFunctions, "", "")
	r := c.route("GET", "/api/scores", "scores")
	if got := strings.Join(toStrings(jsonField(r, "trace")), ","); got != "root,api-a,api-b:/api" {
		t.Errorf("trace %q", got)
	}
	if r.h.Get("x-trace") != "root,api-a,api-b:/api" {
		t.Errorf("x-trace %q (the root middleware edits the response after next())", r.h.Get("x-trace"))
	}
	if jsonField(r, "asset") != "function" {
		t.Errorf("env.ASSETS: %s", r.body)
	}
	// Root middleware also wraps assets that reach the Worker.
	if r := c.get("/files/public/f.txt"); r.code != 200 || r.body != "public file" {
		t.Errorf("an asset under a function route: %d %q", r.code, r.body)
	}
}

// TestPagesFunctionsAssets: files serve as before; next() and unmatched
// paths fall through to env.ASSETS; next(url) fetches another asset;
// passThroughOnException serves the asset on a throw.
func TestPagesFunctionsAssets(t *testing.T) {
	c := startPages(t, pagesFunctions, "", "")
	c.file("/about", "<h1>about</h1>", "text/html; charset=utf-8")
	c.file("/app.js", "console.log(1)", "")
	// A catch-all's next(): the file under it.
	if r := c.get("/files/public/f.txt"); r.code != 200 || r.body != "public file" || r.h.Get("x-trace") != "root" {
		t.Errorf("next() to a file: %d %q %v", r.code, r.body, r.h)
	}
	if r := c.get("/files/public/none.txt"); r.code != 404 || r.body != "root 404" {
		t.Errorf("next() to no file: %d %q", r.code, r.body)
	}
	if r := c.get("/rewrite"); r.code != 200 || r.body != "<h1>about</h1>" {
		t.Errorf("next(url): %d %q", r.code, r.body)
	}
	if r := c.get("/boom"); r.code != 404 || r.body != "root 404" {
		t.Errorf("passThroughOnException: %d %q", r.code, r.body)
	}
	c.status("/no/such/page", 404, "root 404")
}

// TestPagesRoutesJSON: _routes.json decides which paths run the Functions;
// an excluded path serves assets for every method.
func TestPagesRoutesJSON(t *testing.T) {
	c := startPages(t, pagesFunctions, "", `{"version":1,"include":["/api/*"],"exclude":["/api/scores/*"]}`)
	c.route("GET", "/api/users/bo/posts", "posts")
	if r := c.do("DELETE", "/api/scores/1", "workload"); r.code != 405 {
		t.Errorf("excluded DELETE: %d %q, want the assets 405", r.code, r.body)
	}
	if r := c.get("/api/scores/top.txt"); r.body != "a file next to a function" {
		t.Errorf("excluded GET: %d %q", r.code, r.body)
	}
	// /files isn't included: the catch-all never runs.
	c.status("/files/x", 404, "root 404")
	if r := c.do("POST", "/", "workload"); r.code != 405 {
		t.Errorf("POST / not included: %d %q", r.code, r.body)
	}
}

const advancedWorker = `export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    if (u.pathname.startsWith("/api/")) return Response.json({ route: "worker", method: req.method });
    return env.ASSETS.fetch(req);
  },
};`

// TestPagesAdvancedMode: _worker.js is the Worker for every request, and
// calls env.ASSETS; with _routes.json only included paths reach it.
func TestPagesAdvancedMode(t *testing.T) {
	c := startPages(t, nil, advancedWorker, "")
	c.route("GET", "/api/x", "worker")
	c.route("POST", "/api/x", "worker")
	c.file("/about", "<h1>about</h1>", "")
	c.status("/nope", 404, "root 404")

	f := startPages(t, nil, advancedWorker, `{"version":1,"include":["/*"],"exclude":["/api/scores/*"]}`)
	f.route("DELETE", "/api/x", "worker")
	if r := f.do("POST", "/api/scores/top.txt", "workload"); r.code != 405 {
		t.Errorf("excluded POST reached the worker: %d %q", r.code, r.body)
	}
	if r := f.get("/api/scores/top.txt"); r.body != "a file next to a function" {
		t.Errorf("excluded GET: %d %q", r.code, r.body)
	}
}
