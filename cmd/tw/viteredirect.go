package main

// The Cloudflare Vite plugin's redirected config (issue #301;
// docs/wrangler.md#vite). `vite build` writes the Worker it built, with a
// wrangler config of its own, to the build output (dist/<worker>/ or
// build/server/), and points .wrangler/deploy/config.json at that config.
// wrangler deploy follows the pointer; so do tw deploy, dev, policy and
// types. tailworks.jsonc still comes from the project directory.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/container"
)

// viteDeployConfig is where the Vite plugin leaves the pointer, relative
// to the project directory (wrangler's PATH_TO_DEPLOY_CONFIG).
const viteDeployConfig = ".wrangler/deploy/config.json"

// redirectedWranglerConfig reads dir's .wrangler/deploy/config.json and
// returns the config it points at, relative to dir ("" without one), and
// one line per auxiliary Worker it can't deploy.
func redirectedWranglerConfig(dir string) (file string, notes []string, err error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(viteDeployConfig)))
	if os.IsNotExist(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var dc struct {
		ConfigPath       string `json:"configPath"`
		AuxiliaryWorkers []struct {
			ConfigPath string `json:"configPath"`
		} `json:"auxiliaryWorkers"`
	}
	if err := json.Unmarshal(b, &dc); err != nil || dc.ConfigPath == "" {
		why := "no configPath"
		if err != nil {
			why = err.Error()
		}
		return "", nil, fail("bad_request", viteDeployConfig+": "+why).
			withFix("rebuild (for example `npm run build`), or delete " + viteDeployConfig)
	}
	base := filepath.Join(dir, ".wrangler", "deploy")
	abs := dc.ConfigPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, filepath.FromSlash(abs))
	}
	file, err = filepath.Rel(dir, abs)
	if err != nil {
		file = abs
	}
	file = filepath.ToSlash(file)
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return "", nil, fail("bad_request", fmt.Sprintf("%s points at %s, which does not exist", viteDeployConfig, file)).
			withFix("rebuild (for example `npm run build`), or delete " + viteDeployConfig)
	}
	for _, x := range dc.AuxiliaryWorkers {
		notes = append(notes, fmt.Sprintf("%s: auxiliary Worker %s: not deployed (tailworks deploys one Worker per project; deploy it from its own directory)", viteDeployConfig, x.ConfigPath))
	}
	return file, notes, nil
}

// rebaseWranglerPaths rewrites cfg's relative paths, which wrangler
// resolves against the config file, to be relative to the project
// directory: cfgDir is the config's directory relative to it.
func rebaseWranglerPaths(cfg map[string]any, cfgDir string) {
	if cfgDir == "." || cfgDir == "" {
		return
	}
	rebase := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.ToSlash(filepath.Join(cfgDir, filepath.FromSlash(p)))
	}
	// A Dockerfile path stays one (container.IsBuildSource) only with a
	// leading ./ or ../.
	rebaseSource := func(p string) string {
		if !container.IsBuildSource(p) || filepath.IsAbs(p) {
			return p
		}
		r := rebase(p)
		if !strings.HasPrefix(r, "../") {
			r = "./" + r
		}
		return r
	}
	for _, k := range []string{"main", "base_dir", "pages_build_output_dir", "tsconfig"} {
		if s, ok := cfg[k].(string); ok {
			cfg[k] = rebase(s)
		}
	}
	if m, ok := cfg["assets"].(map[string]any); ok {
		if s, ok := m["directory"].(string); ok {
			m["directory"] = rebase(s)
		}
	}
	each := func(k string, f func(map[string]any)) {
		xs, _ := cfg[k].([]any)
		for _, x := range xs {
			if m, ok := x.(map[string]any); ok {
				f(m)
			}
		}
	}
	each("d1_databases", func(m map[string]any) {
		if s, ok := m["migrations_dir"].(string); ok {
			m["migrations_dir"] = rebase(s)
		}
	})
	each("containers", func(m map[string]any) {
		if s, ok := m["image"].(string); ok {
			m["image"] = rebaseSource(s)
		}
		if s, ok := m["image_build_context"].(string); ok {
			m["image_build_context"] = rebase(s)
		}
		if imgs, ok := m["images"].(map[string]any); ok {
			for n, v := range imgs {
				if s, ok := v.(string); ok {
					imgs[n] = rebaseSource(s)
				}
				if im, ok := v.(map[string]any); ok {
					rebaseContainerImage(im, rebase, rebaseSource) // containerbuild.go
				}
			}
		}
	})
}
