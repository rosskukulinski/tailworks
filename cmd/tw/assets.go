package main

// Static assets (wrangler "assets", ADR-050): tw deploy and tw
// dev read assets.directory and put its files in the bundle under
// bundle.AssetsPrefix, outside the module graph. hostd serves them through
// a generated router Worker (hostd/gen/assets.go). docs/wrangler.md#assets.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// assetsAlwaysIgnored are never uploaded (as wrangler: .assetsignore, and
// the usual VCS and OS clutter). _redirects and _headers are uploaded and
// read by the host, not served.
var assetsAlwaysIgnored = map[string]bool{
	".assetsignore": true,
	".DS_Store":     true,
	".git":          true,
	"node_modules":  true,
}

// assetsDir is the app's absolute assets.directory ("" without "assets").
func (a *app) assetsDir() string {
	m, _ := a.Wrangler["assets"].(map[string]any)
	d, _ := m["directory"].(string)
	if d == "" {
		return ""
	}
	if filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(a.Dir, d)
}

// addAssets adds assets.directory's files to files under the assets prefix.
func (a *app) addAssets(files map[string][]byte) error {
	raw, ok := a.Wrangler["assets"]
	if !ok {
		return nil
	}
	file := a.WranglerFile
	if file == "" {
		file = "wrangler config"
	}
	if _, isMap := raw.(map[string]any); !isMap {
		return fail("bad_request", file+": assets: want an object like {\"directory\": \"./public\"}")
	}
	wj, _ := json.Marshal(map[string]any{"assets": raw})
	if err := bundle.CheckAssets(wj, a.assetsOnly()); err != nil {
		return fail("bad_request", file+": "+strings.TrimPrefix(err.Error(), "bundle: wrangler.jsonc "))
	}
	dir := a.assetsDir()
	if dir == "" {
		return fail("bad_request", file+": assets has no directory").withFix(`set "assets": { "directory": "./public" } (the folder of files to serve)`)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fail("bad_request", fmt.Sprintf("%s: assets.directory %s is not a directory", file, rel(a.Dir, dir))).
			withFix("create it, or build your client first (for example `npm run build`)")
	}
	for p := range files {
		if strings.HasPrefix(p, bundle.AssetsPrefix) {
			return fail("bad_request", "the build wrote "+p+", but "+bundle.AssetsPrefix+" is where the bundle carries assets").
				withFix("move that module out of " + bundle.AssetsPrefix)
		}
	}
	ignore := readAssetsIgnore(filepath.Join(dir, ".assetsignore"))
	total := 0
	err := filepath.WalkDir(dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(dir, fp)
		r = filepath.ToSlash(r)
		if r == "." {
			return nil
		}
		skip := assetsAlwaysIgnored[d.Name()] || ignore.match(r, d.IsDir()) || a.pagesSkipAsset(r)
		if skip {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets: not served
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		total += len(b)
		if total > bundle.MaxBundle {
			return fail("bad_request", fmt.Sprintf("assets.directory %s holds more than %d MiB", rel(a.Dir, dir), bundle.MaxBundle>>20)).
				withFix("leave large files out with an .assetsignore, or serve them from R2")
		}
		files[bundle.AssetsPrefix+r] = b
		return nil
	})
	if err != nil {
		if _, ok := err.(*cliErr); ok {
			return err
		}
		return fail("bad_request", "reading assets.directory: "+err.Error())
	}
	// _redirects and _headers: a bad line fails here, before the upload
	// (hostd parses them again; they are not served as files).
	if b, ok := files[bundle.AssetsPrefix+"_redirects"]; ok {
		if _, err := bundle.ParseRedirects(b); err != nil {
			return fail("bad_request", rel(a.Dir, dir)+"/"+err.Error())
		}
	}
	if b, ok := files[bundle.AssetsPrefix+"_headers"]; ok {
		if _, err := bundle.ParseHeaders(b); err != nil {
			return fail("bad_request", rel(a.Dir, dir)+"/"+err.Error())
		}
	}
	return nil
}

// assetsOnly reports an assets-only project: wrangler "assets" and no
// "main". tw bundles no Worker; hostd supplies one (bundle.AssetsOnlyMain).
func (a *app) assetsOnly() bool {
	_, ok := a.Wrangler["assets"]
	return ok && wranglerMain(a.Wrangler) == "" && !a.Pages.hasCode()
}

// checkBundleSize fails when the packed bundle is over what mgmt accepts
// in a deploy request (64 MiB of JSON, so about 48 MiB of bundle).
func checkBundleSize(tb []byte) error {
	if n := base64.StdEncoding.EncodedLen(len(tb)); n > 64<<20-4096 {
		return fail("bad_request", fmt.Sprintf("the bundle is %.1f MiB; a deploy carries at most about 48 MiB (64 MiB as JSON)", float64(len(tb))/(1<<20))).
			withFix("make the assets smaller (an .assetsignore leaves files out), or serve large files from R2")
	}
	return nil
}

// assetsIgnore is an .assetsignore: gitignore-style lines, a subset (no
// "!" negation, no "**").
type assetsIgnore []string

func readAssetsIgnore(p string) assetsIgnore {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out assetsIgnore
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// match reports whether the relative path r (a directory if dir) is ignored.
func (ig assetsIgnore) match(r string, dir bool) bool {
	for _, p := range ig {
		dirOnly := strings.HasSuffix(p, "/")
		p = strings.TrimSuffix(p, "/")
		if dirOnly && !dir {
			continue
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, "/") {
			if ok, _ := path.Match(strings.TrimPrefix(p, "/"), r); ok {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p, path.Base(r)); ok {
			return true
		}
	}
	return false
}
