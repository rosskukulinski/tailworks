package gen_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// assetsWorker answers every request it gets with JSON naming the path;
// /binding/<p> is env.ASSETS.fetch(<p>).
const assetsWorker = `export default { async fetch(req, env) {
  const u = new URL(req.url);
  if (u.pathname.startsWith("/binding/")) { u.pathname = u.pathname.slice(8); return env.ASSETS.fetch(new Request(u, req)); }
  return Response.json({ worker: u.pathname, method: req.method });
} };`

// assetsClassWorker is assetsWorker as a WorkerEntrypoint class.
const assetsClassWorker = `import { WorkerEntrypoint } from "cloudflare:workers";
export default class App extends WorkerEntrypoint { async fetch(req) {
  const u = new URL(req.url);
  if (u.pathname.startsWith("/binding/")) { u.pathname = u.pathname.slice(8); return this.env.ASSETS.fetch(new Request(u, req)); }
  return Response.json({ worker: u.pathname, method: req.method, cls: true });
} }`

var assetFiles = map[string]string{
	"index.html":         "<h1>home</h1>",
	"about.html":         "<h1>about</h1>",
	"docs/index.html":    "<h1>docs</h1>",
	"docs/404.html":      "docs 404",
	"404.html":           "root 404",
	"app.js":             "console.log(1)",
	"a b.txt":            "spaced",
	"api/secret.txt":     "secret",
	"api/public/x.txt":   "public",
	"img/logo.svg":       "<svg/>",
	"nested/deep/f.json": `{"a":1}`,
}

func startAssets(t *testing.T, worker string, assets map[string]any) *assetClient {
	t.Helper()
	return startAssetsWith(t, worker, assets, nil)
}

// startAssetsWith is startAssets with extra asset files (e.g. _redirects);
// worker "" is an assets-only project (no main).
func startAssetsWith(t *testing.T, worker string, assets map[string]any, extra map[string]string) *assetClient {
	t.Helper()
	cfg := map[string]any{"name": "w", "compatibility_date": "2026-04-01", "assets": assets}
	files := map[string][]byte{}
	if worker != "" {
		cfg["main"] = "index.js"
		files["index.js"] = []byte(worker)
	}
	w, _ := json.Marshal(cfg)
	files["wrangler.jsonc"] = w
	for p, b := range assetFiles {
		files[bundle.AssetsPrefix+p] = []byte(b)
	}
	for p, b := range extra {
		files[bundle.AssetsPrefix+p] = []byte(b)
	}
	in := startFiles(t, files)
	return &assetClient{t: t, in: in}
}

type assetClient struct {
	t  *testing.T
	in *inst
}

type assetResp struct {
	code int
	h    http.Header
	body string
}

// worker reports whether the app's Worker answered, and for which path.
func (r assetResp) worker() string {
	var m struct {
		Worker string `json:"worker"`
	}
	if json.Unmarshal([]byte(r.body), &m) != nil {
		return ""
	}
	return m.Worker
}

func (c *assetClient) do(method, path string, kind string, hdr ...string) assetResp {
	c.t.Helper()
	req, _ := http.NewRequest(method, "http://app.local"+path, nil)
	b, _ := json.Marshal(map[string]any{"tailscale": c.in.sealed(), "objectId": "o1", "tw": c.in.ticketed(map[string]any{"kind": kind})})
	req.Header.Set("x-tw-cf", string(b))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	hc := &http.Client{Transport: c.in.hc.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return assetResp{resp.StatusCode, resp.Header, string(rb)}
}

func (c *assetClient) get(path string, hdr ...string) assetResp {
	c.t.Helper()
	return c.do("GET", path, "workload", hdr...)
}

// file asserts path is served as a file with body and content type.
func (c *assetClient) file(path, body, ctype string) assetResp {
	c.t.Helper()
	r := c.get(path)
	if r.code != 200 || r.body != body || (ctype != "" && r.h.Get("content-type") != ctype) || r.h.Get("etag") == "" {
		c.t.Errorf("GET %s: %d %q %v, want 200 %q (%s) with an ETag", path, r.code, r.body, r.h, body, ctype)
	}
	return r
}

func (c *assetClient) redirect(path, to string) {
	c.t.Helper()
	if r := c.get(path); r.code != 307 || r.h.Get("location") != to {
		c.t.Errorf("GET %s: %d location %q, want 307 %q", path, r.code, r.h.Get("location"), to)
	}
}

func (c *assetClient) worker(path string, hdr ...string) {
	c.t.Helper()
	if r := c.get(path, hdr...); r.worker() != path {
		c.t.Errorf("GET %s: %d %q, want the Worker", path, r.code, r.body)
	}
}

func (c *assetClient) status(path string, code int, body string, hdr ...string) {
	c.t.Helper()
	if r := c.get(path, hdr...); r.code != code || r.body != body {
		c.t.Errorf("GET %s: %d %q, want %d %q", path, r.code, r.body, code, body)
	}
}

// TestAssetsAutoTrailingSlash: the defaults (auto-trailing-slash, no
// not_found_handling) with a Worker: files, redirects, MIME types, ETag
// and 304, HEAD, misses and non-GETs to the Worker, env.ASSETS.
func TestAssetsAutoTrailingSlash(t *testing.T) {
	c := startAssets(t, assetsWorker, map[string]any{"directory": "public", "binding": "ASSETS"})
	html := "text/html; charset=utf-8"
	home := c.file("/", "<h1>home</h1>", html)
	c.file("/about", "<h1>about</h1>", html)
	c.redirect("/about.html", "/about")
	c.redirect("/about/", "/about")
	c.redirect("/index.html", "/")
	c.redirect("/docs", "/docs/")
	c.redirect("/docs?x=1", "/docs/?x=1")
	c.file("/docs/", "<h1>docs</h1>", html)
	c.redirect("/docs/index.html", "/docs/")
	c.file("/app.js", "console.log(1)", "text/javascript; charset=utf-8")
	c.file("/img/logo.svg", "<svg/>", "image/svg+xml")
	c.file("/a%20b.txt", "spaced", "text/plain; charset=utf-8")
	c.file("/nested/deep/f.json", `{"a":1}`, "application/json; charset=utf-8")
	c.worker("/nope")
	c.worker("/docs/nope")
	c.worker("/%E0%A4") // not UTF-8: not a file

	// ETag / If-None-Match.
	etag := home.h.Get("etag")
	if r := c.get("/", "if-none-match", etag); r.code != 304 || r.body != "" || r.h.Get("etag") != etag {
		t.Errorf("If-None-Match %s: %d %q", etag, r.code, r.body)
	}
	if r := c.get("/", "if-none-match", `W/`+etag+`, "x"`); r.code != 304 {
		t.Errorf("weak If-None-Match: %d", r.code)
	}
	if r := c.get("/", "if-none-match", `"other"`); r.code != 200 {
		t.Errorf("stale If-None-Match: %d", r.code)
	}
	if c.file("/about", "<h1>about</h1>", html).h.Get("etag") == etag {
		t.Error("two files share an ETag")
	}
	if r := c.do("HEAD", "/about", "workload"); r.code != 200 || r.body != "" || r.h.Get("content-type") != html {
		t.Errorf("HEAD /about: %d %q %v", r.code, r.body, r.h)
	}
	if r := c.do("POST", "/about", "workload"); r.worker() != "/about" {
		t.Errorf("POST /about: %d %q, want the Worker", r.code, r.body)
	}

	// env.ASSETS: a file, a redirect, and none's empty 404.
	c.file("/binding/about", "<h1>about</h1>", html)
	c.redirect("/binding/docs", "/docs/")
	c.status("/binding/nope", 404, "")
}

// TestAssets404PageWorkerFirst: run_worker_first patterns,
// drop-trailing-slash, and 404-page through env.ASSETS.
func TestAssets404PageWorkerFirst(t *testing.T) {
	c := startAssets(t, assetsWorker, map[string]any{
		"directory": "public", "binding": "ASSETS", "html_handling": "drop-trailing-slash",
		"not_found_handling": "404-page", "run_worker_first": []string{"/api/*", "!/api/public/*"},
	})
	c.worker("/api/secret.txt")
	c.file("/api/public/x.txt", "public", "")
	c.file("/docs", "<h1>docs</h1>", "")
	c.redirect("/docs/", "/docs")
	c.redirect("/docs/index.html", "/docs")
	c.redirect("/about.html", "/about")
	c.file("/", "<h1>home</h1>", "")
	// With a Worker, a router miss goes to the Worker, not the 404 page.
	c.worker("/nope")
	c.status("/binding/nope", 404, "root 404")
	c.status("/binding/docs/a/b", 404, "docs 404")
	c.status("/binding/api/secret.txt", 200, "secret")
}

// TestAssetsSPAWorkerFirstAll: run_worker_first true sends everything to
// the Worker; env.ASSETS answers a miss with index.html; force-trailing-slash.
func TestAssetsSPAWorkerFirstAll(t *testing.T) {
	c := startAssets(t, assetsWorker, map[string]any{
		"directory": "public", "binding": "ASSETS", "html_handling": "force-trailing-slash",
		"not_found_handling": "single-page-application", "run_worker_first": true,
	})
	c.worker("/")
	c.worker("/about")
	c.redirect("/binding/about", "/about/")
	c.status("/binding/about/", 200, "<h1>about</h1>")
	c.status("/binding/docs/", 200, "<h1>docs</h1>")
	c.status("/binding/app/route", 200, "<h1>home</h1>")
}

// TestAssetsSPANavigation: in single-page-application mode a browser
// navigation that misses gets index.html; other misses (fetch(), cron,
// objects) reach the Worker. The Worker here is a WorkerEntrypoint class.
func TestAssetsSPANavigation(t *testing.T) {
	c := startAssets(t, assetsClassWorker, map[string]any{"directory": "dist", "binding": "ASSETS", "not_found_handling": "single-page-application"})
	c.file("/about", "<h1>about</h1>", "")
	c.status("/app/route", 200, "<h1>home</h1>", "sec-fetch-mode", "navigate")
	c.worker("/app/route", "sec-fetch-mode", "cors")
	if r := c.get("/app/route"); !strings.Contains(r.body, `"cls":true`) {
		t.Errorf("class Worker: %d %q", r.code, r.body)
	}
	c.status("/binding/app/route", 200, "<h1>home</h1>")
}

// TestGenerateAssetsFiles: the instance dir holds the files under assets/,
// the manifest, and config.capnp's read-only disk service and bindings; an
// app without assets gets none of it.
func TestGenerateAssetsFiles(t *testing.T) {
	gen1 := func(as *bundle.Assets) (string, string) {
		app := &bundle.App{
			Name: "w", Main: "index.js", CompatDate: "2026-04-01",
			Modules: map[string][]byte{"index.js": []byte("export default {};")},
			Classes: map[string]*bundle.Class{}, Assets: as,
		}
		dir := filepath.Join(t.TempDir(), "i")
		if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "d", Workload: "w"}); err != nil {
			t.Fatal(err)
		}
		cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
		return dir, string(cfg)
	}
	_, plain := gen1(nil)
	if strings.Contains(plain, "assets") {
		t.Errorf("an app without assets got assets config:\n%s", plain)
	}
	dir, cfg := gen1(&bundle.Assets{Binding: "STATIC", HTMLHandling: bundle.HTMLNone, NotFound: bundle.NotFoundNone,
		Files: map[string][]byte{"/index.html": []byte("hi"), "/x/y.css": []byte("a{}")}})
	for _, want := range []string{
		`(name = "assets", worker = .assets)`,
		`(name = "assets-disk", disk = (path = "` + filepath.Join(dir, "assets") + `", writable = false))`,
		`(name = "STATIC", service = "assets")`,
		`(name = "__TW_ASSETS", service = (name = "assets", props = (json = "{\"router\":true}")))`,
		`globalOutbound = "deny"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "assets", "x", "y.css")); string(b) != "a{}" {
		t.Errorf("assets/x/y.css: %q", b)
	}
	var m map[string]string
	b, _ := os.ReadFile(filepath.Join(dir, "tw-assets-manifest.json"))
	if json.Unmarshal(b, &m) != nil || len(m) != 2 || m["/index.html"] == "" {
		t.Errorf("manifest: %s", b)
	}
	if mb, _ := os.ReadFile(filepath.Join(dir, "__tw_main.js")); !strings.Contains(string(mb), "withAssets(wrapHandler(T.default, B), false)") {
		t.Errorf("__tw_main.js: %s", mb)
	}
	// microVM guest: the disk path is relative to the guest's app dir.
	app := &bundle.App{Name: "w", Main: "index.js", CompatDate: "2026-04-01", Modules: map[string][]byte{"index.js": []byte("export default {};")},
		Classes: map[string]*bundle.Class{}, Assets: &bundle.Assets{Files: map[string][]byte{}}}
	gdir := filepath.Join(t.TempDir(), "g")
	if _, err := gen.Generate(app, gen.Options{Dir: gdir, DeploymentID: "d", Workload: "w", Guest: &gen.GuestNet{Ingress: "*:1", EgressObj: "10.0.0.1:2", EgressWL: "10.0.0.1:3", HostAPI: "10.0.0.1:4", DOStorage: "/do"}}); err != nil {
		t.Fatal(err)
	}
	if g, _ := os.ReadFile(filepath.Join(gdir, "config.capnp")); !strings.Contains(string(g), `disk = (path = "assets", writable = false)`) {
		t.Errorf("guest config.capnp: disk path not relative")
	}
}

const testRedirects = `# static, dynamic, external, rewrite
/old              /about
/gone             /about       301
/blog/*           /posts/:splat 308
/u/:id/card       /people/:id?tab=card
/ext/*            https://example.com/:splat
/app/*            /index.html  200
/about            /never       301
`

const testHeaders = `/*
  X-Frame-Options: DENY
  X-Shared: root
/img/*
  Cache-Control: public, max-age=31536000, immutable
  X-Shared: img
  ! X-Frame-Options
/nested/:a/*
  X-Seg: :a/:splat
`

// TestAssetsRedirects: _redirects with a Worker: static and dynamic rules,
// status codes, query strings, external targets and 200 rewrites; rules
// run before files (so /about's rule wins over about.html), and never for
// run_worker_first paths.
func TestAssetsRedirects(t *testing.T) {
	c := startAssetsWith(t, assetsWorker, map[string]any{"directory": "public", "binding": "ASSETS", "run_worker_first": []string{"/api/*"}},
		map[string]string{"_redirects": testRedirects + "/api/x /never\n"})
	cases := []struct {
		path, loc string
		code      int
	}{
		{"/old", "/about", 302},
		{"/old?q=1", "/about?q=1", 302},
		{"/gone", "/about", 301},
		{"/blog/2026/hello", "/posts/2026/hello", 308},
		{"/u/42/card?x=1", "/people/42?tab=card", 302},
		{"/ext/a/b", "https://example.com/a/b", 302},
		{"/about", "/never", 301},
	}
	for _, k := range cases {
		if r := c.get(k.path); r.code != k.code || r.h.Get("location") != k.loc {
			t.Errorf("GET %s: %d %q, want %d %q", k.path, r.code, r.h.Get("location"), k.code, k.loc)
		}
	}
	c.status("/app/some/route", 200, "<h1>home</h1>") // 200 rewrite to index.html
	c.worker("/api/x")                                // run_worker_first beats _redirects
	c.worker("/blog")                                 // /blog/* needs the slash
	c.worker("/_redirects")                           // not a file: the Worker gets it
	if r := c.get("/binding/old"); r.code != 302 || r.h.Get("location") != "/about" {
		t.Errorf("env.ASSETS /old: %d %v", r.code, r.h) // env.ASSETS applies them too
	}
}

// TestAssetsHeaders: _headers on file responses: a rule overrides the
// router's default, values join across rules, ! removes, placeholders
// fill in; 304s carry them, Worker responses don't.
func TestAssetsHeaders(t *testing.T) {
	c := startAssetsWith(t, assetsWorker, map[string]any{"directory": "public", "binding": "ASSETS", "not_found_handling": "404-page"},
		map[string]string{"_headers": testHeaders})
	r := c.file("/about", "<h1>about</h1>", "")
	if r.h.Get("x-frame-options") != "DENY" || r.h.Get("x-shared") != "root" || r.h.Get("cache-control") != "public, max-age=0, must-revalidate" {
		t.Errorf("/about headers: %v", r.h)
	}
	r = c.file("/img/logo.svg", "<svg/>", "image/svg+xml")
	if r.h.Get("cache-control") != "public, max-age=31536000, immutable" || r.h.Get("x-shared") != "root, img" || r.h.Get("x-frame-options") != "" {
		t.Errorf("/img headers: %v", r.h)
	}
	if r := c.get("/img/logo.svg", "if-none-match", r.h.Get("etag")); r.code != 304 || r.h.Get("cache-control") != "public, max-age=31536000, immutable" {
		t.Errorf("304: %d %v", r.code, r.h)
	}
	if r := c.file("/nested/deep/f.json", `{"a":1}`, ""); r.h.Get("x-seg") != "deep/f.json" {
		t.Errorf("placeholders: %q", r.h.Get("x-seg"))
	}
	if r := c.get("/nope"); r.worker() != "/nope" || r.h.Get("x-frame-options") != "" {
		t.Errorf("a Worker response got _headers: %v", r.h)
	}
	if r := c.get("/binding/missing"); r.code != 404 || r.h.Get("x-frame-options") != "DENY" {
		t.Errorf("404 page: %d %v", r.code, r.h)
	}
	c.worker("/_headers") // not a file
}

// TestAssetsOnly: no main. Files as usual, not_found_handling for every
// miss (404-page here; SPA below), 405 for other methods, _redirects.
func TestAssetsOnly(t *testing.T) {
	c := startAssetsWith(t, "", map[string]any{"directory": "public", "not_found_handling": "404-page"},
		map[string]string{"_redirects": "/old /about 301\n"})
	c.file("/", "<h1>home</h1>", "")
	c.file("/about", "<h1>about</h1>", "")
	c.redirect("/docs", "/docs/")
	c.status("/nope", 404, "root 404")
	c.status("/docs/x/y", 404, "docs 404")
	if r := c.get("/old"); r.code != 301 || r.h.Get("location") != "/about" {
		t.Errorf("/old: %d %v", r.code, r.h)
	}
	if r := c.do("POST", "/about", "workload"); r.code != 405 || r.h.Get("allow") != "GET, HEAD" {
		t.Errorf("POST: %d %v", r.code, r.h)
	}

	spa := startAssetsWith(t, "", map[string]any{"directory": "dist", "not_found_handling": "single-page-application"}, nil)
	spa.status("/app/deep/route", 200, "<h1>home</h1>") // no navigation header needed: there is no Worker
	spa.file("/app.js", "console.log(1)", "")
	none := startAssetsWith(t, "", map[string]any{"directory": "dist"}, nil)
	none.status("/nope", 404, "")
}
