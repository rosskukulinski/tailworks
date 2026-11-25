package gen

// Static assets (wrangler "assets", ADR-050). For an app with
// assets the instance gets:
//
//	assets/<path>              the files (read-only disk service "assets-disk")
//	tw-assets.js               the router Worker (js/assets.js), service "assets"
//	tw-assets-manifest.json    URL path -> ETag
//	tw-assets-config.json      html_handling, not_found_handling, run_worker_first,
//	                           assets-only, and the compiled _redirects / _headers rules
//	__tw_assets.js             the tenant-side front (js/assets-front.js)
//
// The tenant service binds the router as __TW_ASSETS (props router: true)
// and, with assets.binding, env.<binding> without props.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

//go:embed js/assets.js
var assetsJS string

//go:embed js/assets-front.js
var assetsFrontJS string

// assetsDir is the instance subdirectory holding the asset files.
const assetsDir = "assets"

// writeAssets writes the asset files, manifest, config and both modules.
func writeAssets(app *bundle.App, write func(string, []byte) error) error {
	as := app.Assets
	if as == nil {
		return nil
	}
	manifest := map[string]string{}
	for _, p := range as.AssetPaths() {
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/") {
			return fmt.Errorf("gen: bad asset path %q", p)
		}
		sum := sha256.Sum256(as.Files[p])
		manifest[p] = hex.EncodeToString(sum[:16])
		if err := write(assetsDir+p, as.Files[p]); err != nil {
			return err
		}
	}
	mb, _ := json.Marshal(manifest)
	cb, _ := json.Marshal(map[string]any{
		"htmlHandling":   as.HTMLHandling,
		"notFound":       as.NotFound,
		"runWorkerFirst": as.WorkerFirst,
		"assetsOnly":     as.Only,
		"redirects":      nonNilSlice(as.Redirects),
		"headers":        nonNilSlice(as.Headers),
	})
	for name, b := range map[string][]byte{
		"tw-assets-manifest.json": mb,
		"tw-assets-config.json":   cb,
		"tw-assets.js":            []byte(assetsJS),
		"__tw_assets.js":          []byte(assetsFrontJS),
	} {
		if err := write(name, b); err != nil {
			return err
		}
	}
	// An app with no files still gets the directory the disk service names.
	return write(assetsDir+"/.tw-keep", nil)
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// assetsMain wraps the tenant main module's default export expression.
func assetsMain(app *bundle.App, expr string) string {
	if app.Assets == nil {
		return "export default " + expr + ";\n"
	}
	return fmt.Sprintf("import { withAssets } from \"./__tw_assets.js\";\nexport default withAssets(%s, %v);\n", expr, app.Assets.WorkerFirst.All)
}

// assetsTenantModules is the tenant service's module entry for the front.
func assetsTenantModules(app *bundle.App) string {
	if app.Assets == nil {
		return ""
	}
	return "    (name = \"__tw_assets.js\", esModule = embed \"__tw_assets.js\"),\n"
}

// assetsServices are the router Worker and its disk service. The microVM
// guest runs workerd in its app dir, so its path is relative there.
func assetsServices(app *bundle.App, o Options) string {
	if app.Assets == nil {
		return ""
	}
	dir := assetsDir
	if o.Guest == nil {
		dir, _ = filepath.Abs(filepath.Join(o.Dir, assetsDir))
	}
	return "    (name = \"assets\", worker = .assets),\n" +
		fmt.Sprintf("    (name = \"assets-disk\", disk = (path = %s, writable = false)),\n", cq(dir))
}

// assetsTenantBindings are __TW_ASSETS (the router) and env.<binding>.
func assetsTenantBindings(app *bundle.App) string {
	if app.Assets == nil {
		return ""
	}
	s := "    (name = \"__TW_ASSETS\", service = (name = \"assets\", props = (json = \"{\\\"router\\\":true}\"))),\n"
	if b := app.Assets.Binding; b != "" {
		s += fmt.Sprintf("    (name = %s, service = \"assets\"),\n", cq(b))
	}
	return s
}

// assetsWorker is the router Worker's definition. It has no network: its
// only outbound is the deny service.
func assetsWorker(app *bundle.App) string {
	if app.Assets == nil {
		return ""
	}
	return fmt.Sprintf("const assets :Workerd.Worker = (\n  compatibilityDate = %s,\n  modules = [\n", cq(PlatformCompatDate)) +
		"    (name = \"tw-assets.js\", esModule = embed \"tw-assets.js\"),\n" +
		"    (name = \"tw-assets-manifest.json\", json = embed \"tw-assets-manifest.json\"),\n" +
		"    (name = \"tw-assets-config.json\", json = embed \"tw-assets-config.json\"),\n" +
		"  ],\n  bindings = [ (name = \"FILES\", service = \"assets-disk\") ],\n  globalOutbound = \"deny\",\n);\n\n"
}
