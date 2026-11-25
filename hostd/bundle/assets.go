package bundle

// Static assets (wrangler "assets", ADR-050): tw deploy puts the
// files of assets.directory in the bundle under AssetsPrefix, outside the
// module graph, and the resolved wrangler.jsonc keeps the "assets" settings.
// hostd writes the files to the instance dir and serves them through a
// generated router Worker (hostd/gen/assets.go).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AssetsPrefix is where a bundle carries asset files ("assets/index.html"
// is served at "/index.html"). Files under it are never modules when the
// wrangler config has "assets".
const AssetsPrefix = "assets/"

// AssetsOnlyMain is the module hostd supplies as the Worker of an
// assets-only project (wrangler "assets" with no "main"): the assets router
// answers every GET and HEAD, so only other methods reach it (405).
const AssetsOnlyMain = "__tw_assets_only.js"

const assetsOnlyJS = `export default { fetch() { return new Response(null, { status: 405, headers: { allow: "GET, HEAD" } }); } };
`

// Assets html_handling values (Cloudflare's).
const (
	HTMLAutoTrailingSlash  = "auto-trailing-slash" // the default
	HTMLForceTrailingSlash = "force-trailing-slash"
	HTMLDropTrailingSlash  = "drop-trailing-slash"
	HTMLNone               = "none"
)

// Assets not_found_handling values (Cloudflare's).
const (
	NotFoundNone = "none" // the default
	NotFoundSPA  = "single-page-application"
	NotFound404  = "404-page"
)

// Assets is wrangler.jsonc "assets" plus the files the bundle carries.
type Assets struct {
	// Binding is the env name of the assets Fetcher ("" for none):
	// env.<Binding>.fetch(req) serves a file with not_found_handling.
	Binding string
	// HTMLHandling is one of the HTML* constants (default auto-trailing-slash).
	HTMLHandling string
	// NotFound is one of the NotFound* constants (default none).
	NotFound string
	// WorkerFirst is run_worker_first: All sends every request to the
	// Worker first; otherwise Patterns (wrangler's "/api/*", "!/api/docs/*")
	// pick the paths that do.
	WorkerFirst WorkerFirst
	// Files are the asset files by URL path ("/index.html" -> contents).
	Files map[string][]byte

	// Only is an assets-only project (no "main"): the router answers every
	// request, with not_found_handling for a miss and 405 for a method
	// other than GET and HEAD.
	Only bool

	// Redirects and Headers are the root _redirects and _headers files,
	// parsed (assetrules.go). They are not served as files.
	Redirects []Redirect
	Headers   []HeaderRule
}

// WorkerFirst is assets.run_worker_first.
type WorkerFirst struct {
	All      bool
	Patterns []string
}

// UnmarshalJSON accepts true, false or a list of path patterns.
func (w *WorkerFirst) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &w.All); err == nil {
		return nil
	}
	if err := json.Unmarshal(b, &w.Patterns); err != nil {
		return fmt.Errorf("run_worker_first: want true, false or a list of path patterns")
	}
	for _, p := range w.Patterns {
		if !strings.HasPrefix(strings.TrimPrefix(p, "!"), "/") {
			return fmt.Errorf("run_worker_first: pattern %q must start with / (or !/)", p)
		}
	}
	return nil
}

// MarshalJSON writes the form UnmarshalJSON reads.
func (w WorkerFirst) MarshalJSON() ([]byte, error) {
	if w.All || len(w.Patterns) == 0 {
		return json.Marshal(w.All)
	}
	return json.Marshal(w.Patterns)
}

// AssetPaths returns the asset URL paths, sorted.
func (a *Assets) AssetPaths() []string {
	out := make([]string, 0, len(a.Files))
	for p := range a.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// hasAssetsConfig reports whether wrangler.jsonc (comments stripped) has "assets".
func hasAssetsConfig(wj []byte) bool {
	var w struct {
		Assets json.RawMessage `json:"assets"`
	}
	json.Unmarshal(wj, &w)
	return len(w.Assets) > 0 && string(w.Assets) != "null"
}

// parseAssets reads wrangler.jsonc "assets" (wj, comments stripped) and the
// bundle's AssetsPrefix files into a.Assets. No "assets" key: none.
func parseAssets(wj []byte, files map[string][]byte, a *App) error {
	if !hasAssetsConfig(wj) {
		return nil
	}
	var w struct {
		Assets struct {
			Directory      string      `json:"directory"`
			Binding        string      `json:"binding"`
			HTMLHandling   string      `json:"html_handling"`
			NotFound       string      `json:"not_found_handling"`
			RunWorkerFirst WorkerFirst `json:"run_worker_first"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(wj, &w); err != nil {
		return fmt.Errorf("bundle: wrangler.jsonc assets: %w", err)
	}
	c := w.Assets
	as := &Assets{Binding: c.Binding, HTMLHandling: c.HTMLHandling, NotFound: c.NotFound, WorkerFirst: c.RunWorkerFirst, Files: map[string][]byte{}}
	switch as.HTMLHandling {
	case "":
		as.HTMLHandling = HTMLAutoTrailingSlash
	case HTMLAutoTrailingSlash, HTMLForceTrailingSlash, HTMLDropTrailingSlash, HTMLNone:
	default:
		return fmt.Errorf("bundle: wrangler.jsonc assets.html_handling %q: want %q, %q, %q or %q", as.HTMLHandling, HTMLAutoTrailingSlash, HTMLForceTrailingSlash, HTMLDropTrailingSlash, HTMLNone)
	}
	switch as.NotFound {
	case "":
		as.NotFound = NotFoundNone
	case NotFoundNone, NotFoundSPA, NotFound404:
	default:
		return fmt.Errorf("bundle: wrangler.jsonc assets.not_found_handling %q: want %q, %q or %q", as.NotFound, NotFoundSPA, NotFound404, NotFoundNone)
	}
	if b := as.Binding; b != "" {
		if !identRE.MatchString(b) || strings.HasPrefix(b, "__TW") {
			return fmt.Errorf("bundle: wrangler.jsonc assets.binding %q: want a JS identifier (names starting with __TW are reserved)", b)
		}
		if _, ok := a.Vars[b]; ok {
			return fmt.Errorf("bundle: wrangler.jsonc assets.binding %q is also a var", b)
		}
		// parseAssets runs after the other binding sections are parsed.
		taken := map[string]string{}
		for _, d := range a.Bindings {
			taken[d.Name] = "durable_objects"
		}
		for _, s := range a.Stores {
			taken[s.Binding] = s.Kind
		}
		for _, w := range a.Workflows {
			taken[w.Binding] = "workflows"
		}
		if a.Queues != nil {
			for _, p := range a.Queues.Producers {
				taken[p.Binding] = "queues"
			}
		}
		if what, ok := taken[b]; ok {
			return fmt.Errorf("bundle: wrangler.jsonc assets.binding %q is also a %s binding", b, what)
		}
	}
	for p, b := range files {
		if strings.HasPrefix(p, AssetsPrefix) {
			as.Files["/"+strings.TrimPrefix(p, AssetsPrefix)] = b
		}
	}
	if b, ok := as.Files["/_redirects"]; ok {
		delete(as.Files, "/_redirects")
		var err error
		if as.Redirects, err = ParseRedirects(b); err != nil {
			return fmt.Errorf("bundle: assets %w", err)
		}
	}
	if b, ok := as.Files["/_headers"]; ok {
		delete(as.Files, "/_headers")
		var err error
		if as.Headers, err = ParseHeaders(b); err != nil {
			return fmt.Errorf("bundle: assets %w", err)
		}
	}
	if as.Only = a.Main == AssetsOnlyMain; as.Only && (as.WorkerFirst.All || len(as.WorkerFirst.Patterns) > 0) {
		return fmt.Errorf("bundle: wrangler.jsonc assets.run_worker_first needs a Worker (set \"main\")")
	}
	a.Assets = as
	return nil
}

// CheckAssets validates wrangler JSON's "assets" settings (tw deploy checks
// them before building).
func CheckAssets(wj []byte, assetsOnly bool) error {
	a := &App{}
	if assetsOnly {
		a.Main = AssetsOnlyMain
	}
	return parseAssets(wj, nil, a)
}
