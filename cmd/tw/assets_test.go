package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

func assetsApp(t *testing.T, assets string, files map[string]string) *app {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc": `{"name":"site","main":"src/index.js",` + assets + `}`,
		"src/index.js":   "export default {}",
	})
	writeFiles(t, dir, files)
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestAddAssets: assets.directory's files go in the bundle under assets/,
// except .assetsignore matches and VCS/OS clutter. _headers and _redirects
// go too (the host reads them; they are not served).
func TestAddAssets(t *testing.T) {
	a := assetsApp(t, `"assets":{"directory":"./dist","binding":"ASSETS"}`, map[string]string{
		"dist/index.html":               "<h1>hi</h1>",
		"dist/js/app.js":                "1",
		"dist/.well-known/security.txt": "x",
		"dist/_headers":                 "/*\n  X-A: b",
		"dist/_redirects":               "/a /b",
		"dist/.assetsignore":            "*.map\nprivate/\n/top-only.txt\n# comment",
		"dist/js/app.js.map":            "{}",
		"dist/private/secret.txt":       "s",
		"dist/top-only.txt":             "t",
		"dist/sub/top-only.txt":         "kept",
		"dist/.DS_Store":                "junk",
	})
	files := map[string][]byte{}
	if err := a.addAssets(files); err != nil {
		t.Fatal(err)
	}
	var got []string
	for p := range files {
		got = append(got, p)
	}
	sort.Strings(got)
	want := "assets/.well-known/security.txt,assets/_headers,assets/_redirects,assets/index.html,assets/js/app.js,assets/sub/top-only.txt"
	if strings.Join(got, ",") != want {
		t.Errorf("bundle assets:\n %s\nwant\n %s", strings.Join(got, ","), want)
	}
	if w := strings.Join(a.Warnings, "\n"); strings.Contains(w, "assets") {
		t.Errorf("assets still warned as unsupported: %s", w)
	}
}

func TestAddAssetsErrors(t *testing.T) {
	for _, c := range []struct{ assets, want string }{
		{`"assets":{"directory":"./nope"}`, "is not a directory"},
		{`"assets":{"binding":"ASSETS"}`, "assets has no directory"},
		{`"assets":{"directory":"./public","html_handling":"sometimes"}`, "html_handling"},
		{`"assets":"./public"`, "want an object"},
	} {
		a := assetsApp(t, c.assets, map[string]string{"public/index.html": "x"})
		err := a.addAssets(map[string][]byte{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.assets, err, c.want)
		}
	}
	// A bad _redirects or _headers line fails before the upload, naming it.
	for f, c := range map[string]string{
		"public/_redirects": "/a /b 404",
		"public/_headers":   "  X-Orphan: 1",
	} {
		a := assetsApp(t, `"assets":{"directory":"./public"}`, map[string]string{"public/index.html": "x", f: c})
		err := a.addAssets(map[string][]byte{})
		if err == nil || !strings.Contains(err.Error(), "public/_") || !strings.Contains(err.Error(), ":1:") {
			t.Errorf("%s %q: %v, want the file and line", f, c, err)
		}
	}
	// Assets-only (no main): run_worker_first has no Worker to run.
	a := assetsApp(t, "", nil)
	a.Wrangler = map[string]any{"assets": map[string]any{"directory": "./public", "run_worker_first": true}}
	if err := a.addAssets(map[string][]byte{}); err == nil || !strings.Contains(err.Error(), "needs a Worker") {
		t.Errorf("assets-only run_worker_first: %v", err)
	}
	// A build output under assets/ would be taken for an asset.
	a = assetsApp(t, `"assets":{"directory":"./public"}`, map[string]string{"public/index.html": "x"})
	if err := a.addAssets(map[string][]byte{bundle.AssetsPrefix + "x.js": nil}); err == nil || !strings.Contains(err.Error(), "where the bundle carries assets") {
		t.Errorf("collision: %v", err)
	}
	// No "assets": nothing added, no error.
	a = assetsApp(t, "", nil)
	files := map[string][]byte{}
	if err := a.addAssets(files); err != nil || len(files) != 0 {
		t.Errorf("no assets: %v %v", files, err)
	}
}

// TestSnapshotAssetsDir: tw dev reloads on an edit to an asset, even in
// dist/ (which the source snapshot skips).
func TestSnapshotAssetsDir(t *testing.T) {
	a := assetsApp(t, `"assets":{"directory":"./dist"}`, map[string]string{"dist/index.html": "v1"})
	before := snapshot(a.Dir, a.assetsDir())
	if snapshot(a.Dir, "") == before {
		t.Fatal("the assets dir did not count")
	}
	p := filepath.Join(a.Dir, "dist", "index.html")
	future := time.Now().Add(time.Minute)
	os.WriteFile(p, []byte("v2"), 0o644)
	os.Chtimes(p, future, future)
	if snapshot(a.Dir, a.assetsDir()) == before {
		t.Error("an asset edit did not change the snapshot")
	}
}

// TestBuildAssetsOnly: a project with "assets" and no "main" bundles no
// Worker; the host takes it as assets-only (it supplies the Worker).
func TestBuildAssetsOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":   `{"name":"spa","assets":{"directory":"./dist","not_found_handling":"single-page-application"}}`,
		"dist/index.html":  "<div id=app></div>",
		"dist/_redirects":  "/old /new 301",
		"dist/assets/a.js": "1",
	})
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	tb, js, err := a.build(nil)
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
	if js != 0 || app.Main != bundle.AssetsOnlyMain || app.Assets == nil || !app.Assets.Only {
		t.Fatalf("js %d main %q assets %+v", js, app.Main, app.Assets)
	}
	if _, ok := app.Assets.Files["/assets/a.js"]; !ok || len(app.Assets.Redirects) != 1 {
		t.Errorf("files %v redirects %+v", app.Assets.AssetPaths(), app.Assets.Redirects)
	}
	if _, ok := app.Assets.Files["/_redirects"]; ok {
		t.Error("_redirects is served as a file")
	}
}
