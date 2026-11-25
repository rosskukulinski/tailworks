package main

// Python Workers (docs/python.md, ADR-055): a wrangler config
// whose main is a .py module is packed as it is, with no bundler, the way
// pywrangler uploads one. The files under main's directory become modules
// named relative to it (.py files Python modules, the rest data the code
// reads from its filesystem), and the packages vendored into
// python_modules/ (`uv run pywrangler sync`) ship next to them. hostd runs
// them in Pyodide (hostd/gen/python.go).

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/pyodide"
)

// pythonExternalSDKDate is the first compatibility date at which the
// pinned workerd no longer has the `workers` SDK built in: from it on, the
// SDK is a vendored package (workers-runtime-sdk, which pywrangler
// vendors), or the app sets disable_python_external_sdk.
const pythonExternalSDKDate = "2026-04-21"

// isPythonApp reports whether the wrangler config is a Python Worker.
func isPythonApp(w map[string]any) bool {
	return bundle.IsPythonMain(wranglerMain(w)) || slices.Contains(wranglerFlags(w), bundle.PythonFlag)
}

func wranglerFlags(w map[string]any) []string {
	var out []string
	fl, _ := w["compatibility_flags"].([]any)
	for _, f := range fl {
		if s, ok := f.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// devPyodideDir is tw dev's Pyodide bundle dir: the per-user cache, shared
// by every tw dev run ("" lets hostd use its data dir).
func devPyodideDir() string {
	d, _ := pyodide.UserDir()
	return d
}

// pythonSkipDir: directories under main's directory that hold no modules.
func pythonSkipDir(name string) bool {
	return name == "__pycache__" || name == "node_modules" || name == "dist" || strings.HasPrefix(name, ".") ||
		name == "venv" || strings.HasSuffix(name, ".egg-info")
}

// buildPython packs a Python Worker: main's directory's files and the
// vendored python_modules/ (main's directory's, else the project's), all
// under dist/.
func (a *app) buildPython() (files map[string][]byte, main string, types map[string]string, size int, err error) {
	if a.Opts.builder() == "wrangler" {
		return nil, "", nil, 0, fail("bad_request", "--wrangler and --from-wrangler-out bundle JavaScript; a Python Worker is packed as it is").
			withFix("drop --wrangler / --from-wrangler-out")
	}
	wm := wranglerMain(a.Wrangler)
	flags := wranglerFlags(a.Wrangler)
	if !bundle.IsPythonMain(wm) {
		return nil, "", nil, 0, fail("bad_request", a.WranglerFile+": compatibility_flags has python_workers, but main "+wm+" is not a .py module").
			withFix("point main at the Python entry (e.g. src/entry.py)", "see docs/python.md")
	}
	if !slices.Contains(flags, bundle.PythonFlag) {
		return nil, "", nil, 0, fail("bad_request", a.WranglerFile+": main "+wm+" is a Python module, but compatibility_flags lacks python_workers").
			withFix(`add "python_workers" to compatibility_flags, as on Cloudflare`)
	}
	mainPath := filepath.Join(a.Dir, filepath.FromSlash(strings.TrimPrefix(wm, "./")))
	if _, err := os.Stat(mainPath); err != nil {
		return nil, "", nil, 0, fail("bad_request", "main "+wm+" not found in "+a.Dir)
	}
	base := filepath.Dir(mainPath)
	assetsDir := a.assetsDir() // assets.go: shipped as assets, not as modules
	files, types = map[string][]byte{}, map[string]string{}
	add := func(name string, p string, typ string) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files["dist/"+name] = b
		types["dist/"+name] = typ
		size += len(b)
		return nil
	}
	var sources [][]byte
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != base && (pythonSkipDir(d.Name()) || d.Name() == "python_modules" || p == assetsDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".pyc") || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		if !strings.EqualFold(filepath.Ext(p), ".py") {
			return add(rel, p, "data") // read from the Worker's filesystem (Path(__file__).parent / "x.html")
		}
		if err := add(rel, p, "py"); err != nil {
			return err
		}
		sources = append(sources, files["dist/"+rel])
		return nil
	})
	if err != nil {
		return nil, "", nil, 0, err
	}
	vendored := filepath.Join(base, "python_modules")
	if st, err := os.Stat(vendored); err != nil || !st.IsDir() {
		vendored = filepath.Join(a.Dir, "python_modules")
	}
	hasSDK := false
	if st, err := os.Stat(vendored); err == nil && st.IsDir() {
		err = filepath.WalkDir(vendored, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != vendored && (d.Name() == "__pycache__" || d.Name() == "bin") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".pyc") || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			rel, _ := filepath.Rel(vendored, p)
			rel = filepath.ToSlash(rel)
			if rel == "workers/__init__.py" {
				hasSDK = true
			}
			typ := "data"
			if strings.EqualFold(filepath.Ext(p), ".py") {
				typ = "py"
			}
			return add(bundle.PythonModulesDir+rel, p, typ)
		})
		if err != nil {
			return nil, "", nil, 0, err
		}
	} else {
		for _, f := range []string{"requirements.txt", "pyproject.toml"} {
			if b, err := os.ReadFile(filepath.Join(a.Dir, f)); err == nil && declaresPackages(f, b) {
				a.Warnings = append(a.Warnings, f+": packages are not installed at deploy or at run time; vendor them into python_modules/ (uv run pywrangler sync) to ship them (docs/python.md#packages)")
				break
			}
		}
	}
	date, _ := a.Wrangler["compatibility_date"].(string)
	if date >= pythonExternalSDKDate && !hasSDK && !slices.Contains(flags, "disable_python_external_sdk") && importsWorkersSDK(sources) {
		return nil, "", nil, 0, fail("bad_request", "compatibility_date "+date+": the Python runtime no longer has the `workers` SDK built in (from "+pythonExternalSDKDate+"), and python_modules/ has no workers package").
			withFix("vendor it: uv run pywrangler sync (the workers-runtime-sdk package)", `or add "disable_python_external_sdk" to compatibility_flags`, "or set compatibility_date before "+pythonExternalSDKDate)
	}
	rel, _ := filepath.Rel(base, mainPath)
	return files, "dist/" + filepath.ToSlash(rel), types, size, nil
}

var pyprojectDepsRE = regexp.MustCompile(`(?m)^dependencies\s*=\s*\[([^\]]*)\]`)

// declaresPackages reports whether requirements.txt or pyproject.toml
// (its [project] dependencies) names any package.
func declaresPackages(name string, b []byte) bool {
	if name == "requirements.txt" {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				return true
			}
		}
		return false
	}
	m := pyprojectDepsRE.FindSubmatch(b)
	return m != nil && len(bytes.TrimSpace(m[1])) > 0
}

// importsWorkersSDK reports whether any source imports the workers SDK.
func importsWorkersSDK(sources [][]byte) bool {
	for _, s := range sources {
		for _, l := range strings.Split(string(s), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "from workers ") || strings.HasPrefix(l, "from workers.") || l == "import workers" ||
				strings.HasPrefix(l, "import workers ") || strings.HasPrefix(l, "import workers,") {
				return true
			}
		}
	}
	return false
}
