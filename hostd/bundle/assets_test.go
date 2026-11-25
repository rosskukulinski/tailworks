package bundle

import (
	"strings"
	"testing"
)

func assetsBundle(assets string) map[string][]byte {
	w := `{"name":"w","main":"index.js","vars":{"V":"1"},"durable_objects":{"bindings":[{"name":"DO","class_name":"C"}]},"kv_namespaces":[{"binding":"KV","id":"k"}]` + assets + `}`
	return map[string][]byte{
		"wrangler.jsonc":         []byte(w),
		"index.js":               []byte("export default {}"),
		"assets/index.html":      []byte("<h1>hi</h1>"),
		"assets/app.js":          []byte("console.log(1)"),
		"assets/img/a b.png":     []byte{1, 2},
		"assets/nested/data.txt": []byte("x"),
	}
}

// TestParseAssets: wrangler "assets" makes assets/ files assets (by URL
// path), never modules, with Cloudflare's defaults.
func TestParseAssets(t *testing.T) {
	a, err := Parse(assetsBundle(`,"assets":{"directory":"./public","binding":"ASSETS","run_worker_first":["/api/*","!/api/docs/*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	as := a.Assets
	if as == nil || as.Binding != "ASSETS" || as.HTMLHandling != HTMLAutoTrailingSlash || as.NotFound != NotFoundNone {
		t.Fatalf("assets: %+v", as)
	}
	if got := strings.Join(as.AssetPaths(), ","); got != "/app.js,/img/a b.png,/index.html,/nested/data.txt" {
		t.Errorf("asset paths: %s", got)
	}
	if as.WorkerFirst.All || strings.Join(as.WorkerFirst.Patterns, ",") != "/api/*,!/api/docs/*" {
		t.Errorf("run_worker_first: %+v", as.WorkerFirst)
	}
	for p := range a.Modules {
		if strings.HasPrefix(p, AssetsPrefix) {
			t.Errorf("asset %s became a module", p)
		}
	}
	a, err = Parse(assetsBundle(`,"assets":{"directory":"dist","html_handling":"none","not_found_handling":"single-page-application","run_worker_first":true}`))
	if err != nil || !a.Assets.WorkerFirst.All || a.Assets.HTMLHandling != HTMLNone || a.Assets.NotFound != NotFoundSPA {
		t.Fatalf("%+v %v", a.Assets, err)
	}
}

// TestParseNoAssetsKeepsModules: without "assets", assets/ is an ordinary
// directory of modules (as before assets existed).
func TestParseNoAssetsKeepsModules(t *testing.T) {
	a, err := Parse(assetsBundle(""))
	if err != nil {
		t.Fatal(err)
	}
	if a.Assets != nil {
		t.Errorf("assets: %+v", a.Assets)
	}
	if _, ok := a.Modules["assets/app.js"]; !ok {
		t.Errorf("assets/app.js is not a module without wrangler assets")
	}
}

func TestParseAssetsInvalid(t *testing.T) {
	for cfg, want := range map[string]string{
		`{"html_handling":"auto"}`:                 "html_handling",
		`{"not_found_handling":"index"}`:           "not_found_handling",
		`{"binding":"no-dash"}`:                    "assets.binding",
		`{"binding":"__TW_X"}`:                     "reserved",
		`{"binding":"V"}`:                          "also a var",
		`{"binding":"KV"}`:                         "also a kv binding",
		`{"binding":"DO"}`:                         "durable_objects",
		`{"run_worker_first":"yes"}`:               "run_worker_first",
		`{"run_worker_first":["api/*"]}`:           "must start with /",
		`{"run_worker_first":["/ok","!no-slash"]}`: "must start with /",
	} {
		_, err := Parse(assetsBundle(`,"assets":` + cfg))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error with %q", cfg, err, want)
		}
	}
}
