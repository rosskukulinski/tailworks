package pages

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, s := range files {
		fp := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const h = "export const onRequest = () => new Response('x');"

// TestDiscover: wrangler's filepath routing: index, [param], [[catchall]],
// nested directories (with parameters), _middleware at any level; helper
// files with no handler and node_modules aren't routes.
func TestDiscover(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"index.js":                   h,
		"about.ts":                   "export function onRequestGet() {}",
		"_middleware.js":             h,
		"api/_middleware.ts":         h,
		"api/index.js":               h,
		"api/scores/[id].js":         "export const onRequestDelete = () => {}",
		"api/users/[user]/posts.mjs": h,
		"files/[[path]].js":          h,
		"lib/db.js":                  "export const db = 1;",
		"types.d.ts":                 "export type onRequest = 1;",
		"node_modules/x/index.js":    h,
		".hidden/index.js":           h,
		"README.md":                  "onRequest",
	})
	got, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{File: "_middleware.js", RoutePath: "/", MountPath: "/", Middleware: true},
		{File: "about.ts", RoutePath: "/about", MountPath: "/"},
		{File: "api/_middleware.ts", RoutePath: "/api", MountPath: "/api", Middleware: true},
		{File: "api/index.js", RoutePath: "/api", MountPath: "/api"},
		{File: "api/scores/[id].js", RoutePath: "/api/scores/:id", MountPath: "/api/scores"},
		{File: "api/users/[user]/posts.mjs", RoutePath: "/api/users/:user/posts", MountPath: "/api/users/:user"},
		{File: "files/[[path]].js", RoutePath: "/files/:path*", MountPath: "/files"},
		{File: "index.js", RoutePath: "/", MountPath: "/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes:\n got %+v\nwant %+v", got, want)
	}
	if p := Patterns(got); !reflect.DeepEqual(p, []string{"/*"}) {
		t.Errorf("a root middleware covers everything: %v", p)
	}
}

func TestDiscoverErrors(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"catch-all not last":         {"[[a]]/b.js": h},
		"partial param":              {"api/v[id].js": h},
		"bad param name":             {"api/[a-b].js": h},
		"middleware under catch-all": {"[[a]]/_middleware.js": h},
	} {
		if _, err := Discover(writeTree(t, files)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestPatterns: wrangler's generated _routes.json include list.
func TestPatterns(t *testing.T) {
	routes := []Route{
		{RoutePath: "/api/scores", MountPath: "/api/scores"},
		{RoutePath: "/api/scores/:id", MountPath: "/api/scores"},
		{RoutePath: "/api/users/:user/posts", MountPath: "/api/users/:user"},
		{RoutePath: "/admin", MountPath: "/admin", Middleware: true},
		{RoutePath: "/admin/stats", MountPath: "/admin"},
		{RoutePath: "/files/:path*", MountPath: "/files"},
		{RoutePath: "/", MountPath: "/"},
	}
	want := []string{"/", "/admin", "/admin/*", "/api/scores", "/api/scores/*", "/api/users/*", "/files/*"}
	if got := Patterns(routes); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	var many []Route
	for i := 0; i < MaxRules+1; i++ {
		many = append(many, Route{RoutePath: "/r" + strings.Repeat("x", i)})
	}
	if got := Patterns(many); !reflect.DeepEqual(got, []string{"/*"}) {
		t.Errorf("over the limit: %d patterns", len(got))
	}
}

func TestParseRoutesJSON(t *testing.T) {
	r, err := ParseRoutesJSON([]byte(`{"version":1,"include":["/api/*"],"exclude":["/api/docs/*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Patterns(); !reflect.DeepEqual(got, []string{"/api/*", "!/api/docs/*"}) {
		t.Errorf("patterns %v", got)
	}
	big := `{"version":1,"include":["/a"` + strings.Repeat(`,"/a"`, MaxRules) + `]}`
	for _, bad := range []string{`{`, `{"version":2,"include":["/*"]}`, `{"version":1}`, `{"version":1,"include":["api"]}`, big} {
		if _, err := ParseRoutesJSON([]byte(bad)); err == nil {
			t.Errorf("%.40s: no error", bad)
		}
	}
}

func TestEnvNames(t *testing.T) {
	src := `
export async function onRequestGet({ env, params }) {
  const row = await env.DB.prepare("x").first();
  const k = context.env.API_KEY;
  const { BUCKET, KV: kv, ...rest } = env;
  const { Q = 1 } = context.env;
  const n = process.env.NODE_ENV, m = import.meta.env.MODE;
  return env["TOKEN"];
}`
	want := []string{"API_KEY", "BUCKET", "DB", "KV", "Q", "TOKEN"}
	if got := EnvNames([]byte(src)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestFunctionsEntry: the generated module imports each route by absolute
// path and hands the table, binding and filter to the runtime.
func TestFunctionsEntry(t *testing.T) {
	src := string(FunctionsEntry("/app/functions", []Route{{File: "api/[id].js", RoutePath: "/api/:id", MountPath: "/api"}}, "ASSETS",
		&RoutesJSON{Version: 1, Include: []string{"/api/*"}}))
	for _, want := range []string{
		`import * as __f0 from "/app/functions/api/[id].js";`,
		`{ file: "api/[id].js", routePath: "/api/:id", mountPath: "/api", middleware: false, module: __f0 }`,
		`export default __twPagesFunctions(__twPagesRoutes, "ASSETS", {"exclude":[],"include":["/api/*"]});`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("entry lacks %s:\n%s", want, src)
		}
	}
}
