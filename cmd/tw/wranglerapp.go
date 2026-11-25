package main

// How an app is loaded and bundled from its wrangler config (issue #19;
// docs/wrangler.md): --env, --wrangler (bundle with `wrangler deploy
// --dry-run --outdir`) and --from-wrangler-out (a prebuilt outdir).

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
)

// appOpts are the wrangler-related deploy/dev flags.
type appOpts struct {
	Env         string // wrangler environment (--env): env.<name> in the config
	Wrangler    bool   // bundle with the wrangler CLI (--wrangler)
	WranglerOut string // bundle from a prebuilt `wrangler deploy --dry-run --outdir` (--from-wrangler-out)

	// SourceMap: deploy follows upload_source_maps; tw dev sets it (sourcemap.go).
	SourceMap sourceMapMode

	// DevBuild: tw dev runs build.command itself, only when build.watch_dir
	// changes (buildcmd.go), so a.build doesn't.
	DevBuild bool
}

// builder names the bundler for messages and warnings.
func (o appOpts) builder() string {
	if o.Wrangler || o.WranglerOut != "" {
		return "wrangler"
	}
	return "esbuild"
}

// addAppFlags registers --env, --wrangler and --from-wrangler-out.
func addAppFlags(f *flags) *appOpts {
	o := &appOpts{}
	f.StringVar(&o.Env, "env", "", "wrangler environment: use env.<name> from the wrangler config, as `wrangler --env` does (the workload is named <name>-<env> unless the env sets a name)")
	f.BoolVar(&o.Wrangler, "wrangler", false, "bundle with the wrangler CLI (wrangler deploy --dry-run --outdir; nothing is sent to Cloudflare) instead of the built-in esbuild")
	f.StringVar(&o.WranglerOut, "from-wrangler-out", "", "deploy a bundle wrangler already built (the --outdir of `wrangler deploy --dry-run`)")
	return o
}

// errNoWrangler: the app dir has no wrangler config.
type errNoWrangler struct{ dir string }

func (e errNoWrangler) Error() string { return "no wrangler config in " + e.dir }

// loadWrangler reads the app's wrangler config into a.Wrangler (with
// a.Opts.Env applied) and returns the workload name it implies: base
// (tailworks.jsonc "name", else wrangler "name", else the directory name), or
// for an environment "<base>-<env>" unless the env sets its own name.
func (a *app) loadWrangler(twName string) (string, error) {
	file, shadowed := findWranglerConfig(a.Dir)
	redirect, auxNotes, err := redirectedWranglerConfig(a.Dir) // viteredirect.go
	if err != nil {
		return "", err
	}
	if redirect != "" {
		if a.Opts.Env != "" {
			return "", fail("bad_request", fmt.Sprintf("--env %s: %s is the config `vite build` already wrote for one environment (%s)", a.Opts.Env, redirect, viteDeployConfig)).
				withFix("CLOUDFLARE_ENV="+a.Opts.Env+" npm run build, then tw deploy without --env", "or delete "+viteDeployConfig+" to use the project's own wrangler config")
		}
		file, shadowed = redirect, nil
		a.Warnings = append(a.Warnings, fmt.Sprintf("using %s, the config `vite build` wrote (%s), as wrangler deploy does", file, viteDeployConfig))
		a.Warnings = append(a.Warnings, auxNotes...)
	}
	if file == "" {
		return "", errNoWrangler{a.Dir}
	}
	a.WranglerFile = file
	for _, s := range shadowed {
		a.Warnings = append(a.Warnings, fmt.Sprintf("%s: ignored: %s is used (wrangler's order: %s)", s, file, strings.Join(wranglerConfigNames, ", ")))
	}
	cfg, err := readWranglerConfig(filepath.Join(a.Dir, file))
	if err != nil {
		return "", fail("bad_request", file+": "+err.Error())
	}
	a.WranglerName, _ = cfg["name"].(string)
	rebaseWranglerPaths(cfg, filepath.Dir(file)) // viteredirect.go: "." unless redirected
	w, explicit, notes, err := applyWranglerEnv(cfg, a.Opts.Env, file)
	if err != nil {
		return "", err
	}
	a.Wrangler = w
	a.Warnings = append(a.Warnings, notes...)
	a.Warnings = append(a.Warnings, wranglerWarnings(file, w, a.builder())...)
	if err := a.detectPages(); err != nil { // pages.go
		return "", err
	}
	base := appBaseName(twName, a.WranglerName, a.Dir) // appworkload.go
	switch {
	case a.Opts.Env == "":
		return base, nil
	case explicit:
		n, _ := w["name"].(string)
		return n, nil
	}
	return base + "-" + a.Opts.Env, nil
}

// printWarnings writes the app's warnings, one line each, to w.
func (a *app) printWarnings(w io.Writer) {
	for _, s := range a.Warnings {
		fmt.Fprintf(w, "› %s\n", s)
	}
}

// buildEsbuild bundles with the built-in esbuild, honouring wrangler's
// define, alias, rules, minify, keep_names, jsx_* and tsconfig.
func (a *app) buildEsbuild(out string, np []string) (files map[string][]byte, types map[string]string, js int, err error) {
	o := esbundle.Options{Dir: a.Dir, Main: wranglerMain(a.Wrangler), Outfile: filepath.Join(out, "index.js"), NodePaths: np}
	if a.Pages.hasCode() {
		if o.Main, err = a.pagesEntry(out); err != nil { // pages.go
			return nil, nil, 0, err
		}
	}
	if err := esbuildWranglerOptions(a.Wrangler, &o); err != nil {
		return nil, nil, 0, fail("bad_request", a.WranglerFile+": "+err.Error())
	}
	o.SourceMap, o.SourcesContent = a.wantSourceMap() // sourcemap.go
	r, err := esbundle.BuildApp(o)
	if ee := emailImportFailure(err); ee != nil { // emailimport.go
		return nil, nil, 0, ee
	}
	if err != nil {
		return nil, nil, 0, withBundleFix(fail("build_failed", "esbuild failed for "+a.Dir, strings.Split(strings.TrimSpace(err.Error()), "\n")...), err.Error()) // sourcehints.go
	}
	b, err := os.ReadFile(o.Outfile)
	if err != nil {
		return nil, nil, 0, err
	}
	files = map[string][]byte{"dist/index.js": b}
	if err := a.addSourceMap(o, files, "dist/index.js"); err != nil {
		return nil, nil, 0, err
	}
	for name, m := range r.Modules {
		files["dist/"+name] = m.Data
		if types == nil {
			types = map[string]string{}
		}
		types["dist/"+name] = m.Type
	}
	return files, types, len(b), nil
}

// wranglerBin finds the wrangler CLI: $TW_WRANGLER, the project's own
// node_modules/.bin/wrangler (in the app dir or a parent), then PATH.
func wranglerBin(dir string) (string, error) {
	if p := os.Getenv("TW_WRANGLER"); p != "" {
		return p, nil
	}
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, "node_modules", ".bin", "wrangler")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if p, err := exec.LookPath("wrangler"); err == nil {
		return p, nil
	}
	return "", fail("wrangler_required", "--wrangler needs the wrangler CLI (not in node_modules/.bin or on PATH)").
		withFix("npm install --save-dev wrangler", "or bundle with tailworks' built-in esbuild: drop --wrangler")
}

// wranglerEnv is the environment wrangler runs in. A project's own
// wrangler (node_modules/.bin) gets that directory first on PATH, as under
// `npm run deploy`, so a "build" command that calls a locally installed
// tool (esbuild, tsc) finds it.
func wranglerEnv(bin string) []string {
	env := os.Environ()
	dir := filepath.Dir(bin)
	if filepath.Base(dir) != ".bin" || filepath.Base(filepath.Dir(dir)) != "node_modules" {
		return env
	}
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + v
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// runWrangler runs `wrangler deploy --dry-run --outdir <outdir>` in the
// app dir: wrangler bundles locally and sends nothing to Cloudflare.
func (a *app) runWrangler(outdir string) error {
	bin, err := wranglerBin(a.Dir)
	if err != nil {
		return err
	}
	// --env= (empty) is wrangler's spelling for "the top level", and keeps
	// it from warning that environments exist but none was chosen.
	args := []string{"deploy", "--dry-run", "--outdir", outdir, "--config", a.WranglerFile, "--env=" + a.Opts.Env}
	cmd := exec.Command(bin, args...)
	cmd.Dir = a.Dir
	cmd.Env = append(wranglerEnv(bin), "WRANGLER_SEND_METRICS=false")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fail("build_failed", "wrangler "+strings.Join(args, " ")+" failed in "+a.Dir, tailLines(string(b), 20)...)
	}
	return nil
}

// tailLines is the last n non-empty lines of s.
func tailLines(s string, n int) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// buildWrangler bundles with wrangler (or reads a.Opts.WranglerOut) and
// returns the bundle files under dist/, the entry and the module types.
func (a *app) buildWrangler(tmp string) (files map[string][]byte, main string, types map[string]string, js int, err error) {
	outdir := a.Opts.WranglerOut
	if outdir == "" {
		outdir = filepath.Join(tmp, "wrangler-out")
		if err := a.runWrangler(outdir); err != nil {
			return nil, "", nil, 0, err
		}
	} else if !filepath.IsAbs(outdir) {
		outdir, _ = filepath.Abs(outdir)
	}
	var rules []esbundle.Rule
	if rs, ok := a.Wrangler["rules"]; ok {
		b, _ := json.Marshal(rs)
		json.Unmarshal(b, &rules)
	}
	return readWranglerOut(outdir, wranglerMain(a.Wrangler), rules)
}

// readWranglerOut packs a wrangler --outdir: the entry module (named after
// main, e.g. src/index.ts → index.js) and every other module, typed by
// wrangler's rules (the config's, then wrangler's defaults; matched on the
// file name with the hash prefix stripped) or by extension. README.md and
// source maps are left out.
func readWranglerOut(outdir, main string, userRules []esbundle.Rule) (files map[string][]byte, entry string, types map[string]string, js int, err error) {
	rules := esbundle.EffectiveRules(userRules)
	var rels []string
	err = filepath.WalkDir(outdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(outdir, p)
		rel = filepath.ToSlash(rel)
		if rel == "README.md" || strings.HasSuffix(rel, ".map") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return nil, "", nil, 0, fail("bad_request", "can't read the wrangler output "+outdir+": "+err.Error()).
			withFix("wrangler deploy --dry-run --outdir " + outdir)
	}
	sort.Strings(rels)
	want := strings.TrimSuffix(filepath.Base(main), filepath.Ext(main)) + ".js"
	var tops []string
	for _, r := range rels {
		if r == want {
			entry = r
		}
		if !strings.Contains(r, "/") && (strings.HasSuffix(r, ".js") || strings.HasSuffix(r, ".mjs")) && !esbundle.HashedRE.MatchString(r) {
			tops = append(tops, r)
		}
	}
	if entry == "" && len(tops) == 1 {
		entry = tops[0]
	}
	if entry == "" {
		return nil, "", nil, 0, fail("bad_request", fmt.Sprintf("no entry module in the wrangler output %s (want %s; found %s)", outdir, want, strings.Join(tops, ", "))).
			withFix("wrangler deploy --dry-run --outdir " + outdir)
	}
	files = map[string][]byte{}
	types = map[string]string{}
	for _, r := range rels {
		b, err := os.ReadFile(filepath.Join(outdir, filepath.FromSlash(r)))
		if err != nil {
			return nil, "", nil, 0, err
		}
		p := "dist/" + r
		files[p] = b
		if r == entry {
			js = len(b)
			continue
		}
		switch strings.ToLower(filepath.Ext(r)) {
		case ".js", ".mjs":
			types[p] = "js"
			continue
		case ".cjs":
			return nil, "", nil, 0, fail("bad_request", "the wrangler output has a CommonJS module ("+r+"); tailworks ships ES modules only").
				withFix("bundle the dependency (drop no_bundle / the CommonJS rule)")
		}
		if t, ok := esbundle.MatchRule(rules, "./"+esbundle.HashedRE.ReplaceAllString(filepath.Base(r), "")); ok && esbundle.ModuleType(t) != "" {
			types[p] = esbundle.ModuleType(t)
			continue
		}
		types[p] = bundle.KindByExt(r)
	}
	return files, "dist/" + entry, types, js, nil
}

// packWorkerd adds tw-bundle.json, tailworks.jsonc and the resolved wrangler
// config (as wrangler.jsonc, with --var overrides) to files and packs them.
func (a *app) packWorkerd(files map[string][]byte, main string, types map[string]string, vars map[string]string) ([]byte, error) {
	if err := a.addAssets(files); err != nil { // assets.go
		return nil, err
	}
	tb := map[string]any{"main": main}
	if len(types) > 0 {
		tb["modules"] = types
	}
	files["tw-bundle.json"], _ = json.Marshal(tb)
	files["tailworks.jsonc"] = a.TwRaw
	w := map[string]any{}
	for k, v := range a.Wrangler {
		w[k] = v
	}
	if len(vars) > 0 {
		v, _ := w["vars"].(map[string]any)
		nv := map[string]any{}
		for k, x := range v {
			nv[k] = x
		}
		for k, x := range vars {
			nv[k] = x
		}
		w["vars"] = nv
	}
	files["wrangler.jsonc"], _ = json.MarshalIndent(w, "", "  ")
	tb2, err := bundle.Pack(files)
	if err != nil {
		return nil, err
	}
	return tb2, checkBundleSize(tb2)
}
