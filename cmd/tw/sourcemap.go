package main

// Source maps (issue #42). The built-in esbuild writes dist/index.js.map
// next to the bundle (positions and names, no sources), and the bundle
// ships it; hostd maps every uncaught exception's stack back to src/…
// before it writes the request record (hostd/stackmap.go), so `tw dev`
// and `tw logs` show `src/index.ts:5:11`, as `wrangler dev` does, not
// `dist/index.js:129:54`. wrangler's `upload_source_maps: false` leaves the
// map out of a `tw deploy` (tw dev always keeps it). `tw dev
// --inspector-port` also inlines the map with the sources, so a debugger
// shows the TypeScript.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/esbundle"
)

// sourceMapMode is how an app build treats source maps (appOpts.SourceMap).
type sourceMapMode int

const (
	sourceMapConfig sourceMapMode = iota // deploy: on unless upload_source_maps is false
	sourceMapDev                         // tw dev: always on
	sourceMapInline                      // tw dev --inspector-port: also inlined, with the sources
)

// devSourceMapMode is tw dev's: always a map; inlined with the sources
// when a debugger may attach (--inspector-port).
func devSourceMapMode(inspectorPort int) sourceMapMode {
	if inspectorPort > 0 {
		return sourceMapInline
	}
	return sourceMapDev
}

// wantSourceMap reports whether the build writes a map, and with sources.
func (a *app) wantSourceMap() (on, content bool) {
	switch a.Opts.SourceMap {
	case sourceMapDev:
		return true, false
	case sourceMapInline:
		return true, true
	}
	if v, ok := a.Wrangler["upload_source_maps"].(bool); ok && !v {
		return false, false
	}
	return true, false
}

// addSourceMap reads the map esbuild wrote next to o.Outfile, rewrites its
// sources relative to the app dir (esbuild makes them relative to the temp
// out dir) and adds it to files as <main>.map. With sourceMapInline it is
// also appended to the module as a data: URL, which a debugger reads
// without fetching anything.
func (a *app) addSourceMap(o esbundle.Options, files map[string][]byte, main string) error {
	if !o.SourceMap {
		return nil
	}
	raw, err := os.ReadFile(o.Outfile + ".map")
	if err != nil {
		return nil // nothing to map (esbuild writes no map for an empty bundle)
	}
	b, err := rewriteMapSources(raw, filepath.Dir(o.Outfile), a.Dir)
	if err != nil {
		return err
	}
	files[main+".map"] = b
	if a.Opts.SourceMap == sourceMapInline {
		js := files[main]
		js = append(js[:len(js):len(js)], "\n//# sourceMappingURL=data:application/json;base64,"...)
		js = append(js, base64.StdEncoding.EncodeToString(b)...)
		files[main] = append(js, '\n')
	}
	return nil
}

// rewriteMapSources makes a map's sources relative to appDir: src/index.ts,
// node_modules/hono/dist/index.js. A source outside the app (tailworks' own
// packages) is shown from its last node_modules/ on.
func rewriteMapSources(raw []byte, outDir, appDir string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	srcs, _ := m["sources"].([]any)
	for i, s := range srcs {
		p, ok := s.(string)
		if !ok || strings.Contains(p, ":") && !filepath.IsAbs(p) {
			continue // a plugin namespace ("tw-stub:email")
		}
		abs := p
		if !filepath.IsAbs(p) {
			abs = filepath.Join(outDir, filepath.FromSlash(p))
		}
		rel, err := filepath.Rel(appDir, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rel = abs
			if j := strings.LastIndex(abs, string(filepath.Separator)+"node_modules"+string(filepath.Separator)); j >= 0 {
				rel = abs[j+1:]
			}
		}
		srcs[i] = filepath.ToSlash(rel)
	}
	delete(m, "sourceRoot")
	return json.Marshal(m)
}
