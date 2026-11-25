package main

// Pre-built no_bundle Workers (issue #301; docs/wrangler.md#no-bundle). A
// config with "no_bundle": true and ES-module rules that cover only .js and
// .mjs, as the Cloudflare Vite plugin writes next to its build output, holds
// modules that are already built. The built-in path packs them as they are,
// the way wrangler does (find_additional_modules under base_dir), instead
// of running esbuild over them.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/esbundle"
)

// builderPrebuilt is app.builder() for a pre-built no_bundle Worker.
const builderPrebuilt = "prebuilt"

// prebuiltESMGlobs are the ES-module globs a pre-built Worker may use.
var prebuiltESMGlobs = map[string]bool{"**/*.js": true, "**/*.mjs": true}

// wranglerRules are the config's "rules" (nil when unset or malformed;
// esbuildWranglerOptions reports a malformed one).
func wranglerRules(w map[string]any) []esbundle.Rule {
	rs, ok := w["rules"]
	if !ok {
		return nil
	}
	var rules []esbundle.Rule
	b, _ := json.Marshal(rs)
	if json.Unmarshal(b, &rules) != nil {
		return nil
	}
	return rules
}

// prebuiltNoBundle reports a pre-built Worker: no_bundle, at least one
// ESModule rule, every ESModule glob **/*.js or **/*.mjs, and otherwise only
// rule types tailworks ships (Text, Data, CompiledWasm).
func prebuiltNoBundle(w map[string]any) bool {
	if nb, _ := w["no_bundle"].(bool); !nb || wranglerMain(w) == "" {
		return false
	}
	esm := false
	for _, r := range wranglerRules(w) {
		switch r.Type {
		case "ESModule":
			for _, g := range r.Globs {
				if !prebuiltESMGlobs[g] {
					return false
				}
			}
			esm = true
		case "Text", "Data", "CompiledWasm":
		default:
			return false
		}
	}
	return esm
}

// builder names how the app is bundled: "wrangler", "prebuilt" or "esbuild".
func (a *app) builder() string {
	b := a.Opts.builder()
	if b == "esbuild" && prebuiltNoBundle(a.Wrangler) {
		return builderPrebuilt
	}
	return b
}

// buildPrebuilt packs a pre-built no_bundle Worker: main, and (unless
// find_additional_modules is false) every file under base_dir (default:
// main's directory) that a rule types, at its path relative to base_dir.
// Other files (the build's wrangler.json, .vite/manifest.json, source
// maps) are left out, as wrangler leaves them out.
func (a *app) buildPrebuilt() (files map[string][]byte, main string, types map[string]string, js int, err error) {
	mainAbs := filepath.Join(a.Dir, filepath.FromSlash(wranglerMain(a.Wrangler)))
	base := filepath.Dir(mainAbs)
	if bd, _ := a.Wrangler["base_dir"].(string); bd != "" {
		base = filepath.Join(a.Dir, filepath.FromSlash(bd))
	}
	entry, err := filepath.Rel(base, mainAbs)
	if err != nil || strings.HasPrefix(entry, "..") {
		return nil, "", nil, 0, fail("bad_request", fmt.Sprintf("%s: main %s is not inside base_dir %s", a.WranglerFile, rel(a.Dir, mainAbs), rel(a.Dir, base)))
	}
	entry = filepath.ToSlash(entry)
	b, err := os.ReadFile(mainAbs)
	if err != nil {
		return nil, "", nil, 0, fail("bad_request", fmt.Sprintf("%s: main %s: %v", a.WranglerFile, rel(a.Dir, mainAbs), err)).
			withFix("build it first (for example `npm run build`)")
	}
	files = map[string][]byte{"dist/" + entry: b}
	types = map[string]string{}
	if find, ok := a.Wrangler["find_additional_modules"].(bool); ok && !find {
		return files, "dist/" + entry, types, len(b), nil
	}
	rules := esbundle.EffectiveRules(wranglerRules(a.Wrangler))
	var rels []string
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		r, _ := filepath.Rel(base, p)
		if r = filepath.ToSlash(r); r != entry && !strings.HasSuffix(r, ".map") {
			rels = append(rels, r)
		}
		return nil
	})
	if err != nil {
		return nil, "", nil, 0, fail("bad_request", "reading the pre-built Worker in "+rel(a.Dir, base)+": "+err.Error())
	}
	sort.Strings(rels)
	for _, r := range rels {
		t, ok := esbundle.MatchRule(rules, r)
		if !ok || esbundle.ModuleType(t) == "" {
			continue // not a module (wrangler.json, .vite/manifest.json, …)
		}
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(r)))
		if err != nil {
			return nil, "", nil, 0, err
		}
		files["dist/"+r] = data
		types["dist/"+r] = esbundle.ModuleType(t)
	}
	return files, "dist/" + entry, types, len(b), nil
}
