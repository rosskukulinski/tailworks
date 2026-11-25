package main

// The wrangler config's custom build (issue #495; docs/wrangler.md#custom-builds):
// tw deploy, tw dev and tw policy run build.command themselves before the
// built-in esbuild bundles, as wrangler does before its own bundler. It is
// the project's own command (like `npm run build`), run in build.cwd with
// the project's node_modules/.bin on PATH. --wrangler leaves it to wrangler;
// a Pages project, a pre-built no_bundle Worker and a runtime "node" app
// don't run it.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// wranglerBuild is the config's "build" table.
type wranglerBuild struct {
	Command  string   // build.command ("" for none)
	Cwd      string   // build.cwd, absolute (default: the config's directory)
	WatchDir []string // build.watch_dir, absolute (default ./src, as wrangler)
}

// parseWranglerBuild reads w["build"]; paths are relative to cfgDir, the
// wrangler config's directory.
func parseWranglerBuild(w map[string]any, cfgDir string) wranglerBuild {
	m, _ := w["build"].(map[string]any)
	b := wranglerBuild{Cwd: cfgDir}
	b.Command, _ = m["command"].(string)
	b.Command = strings.TrimSpace(b.Command)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cfgDir, filepath.FromSlash(p))
	}
	if c, _ := m["cwd"].(string); c != "" {
		b.Cwd = abs(c)
	}
	switch wd := m["watch_dir"].(type) {
	case string:
		b.WatchDir = []string{abs(wd)}
	case []any:
		for _, v := range wd {
			if s, ok := v.(string); ok && s != "" {
				b.WatchDir = append(b.WatchDir, abs(s))
			}
		}
	}
	if len(b.WatchDir) == 0 {
		b.WatchDir = []string{abs("src")}
	}
	return b
}

// customBuild is the build.command tailworks runs for this app, or the zero
// value when there is none or another tool owns it: wrangler (--wrangler
// runs it itself; --from-wrangler-out is already built), a Pages project,
// a pre-built no_bundle Worker and a runtime "node" app.
func (a *app) customBuild() wranglerBuild {
	if a.Runtime == bundle.RuntimeNode || a.Pages != nil || a.builder() != "esbuild" {
		return wranglerBuild{}
	}
	return parseWranglerBuild(a.Wrangler, filepath.Join(a.Dir, filepath.Dir(a.WranglerFile)))
}

// buildCommandOut is where the build's own output goes: stderr, so that
// --json keeps stdout for JSON.
var buildCommandOut io.Writer = os.Stderr

// prebuild runs build.command before a.build bundles, unless tw dev runs
// it (devBuild).
func (a *app) prebuild() error {
	if a.Opts.DevBuild {
		return nil
	}
	return a.runBuildCommand()
}

// runBuildCommand runs build.command (when customBuild has one), streaming
// its output. A failure fails the deploy with build_command_failed.
func (a *app) runBuildCommand() error {
	b := a.customBuild()
	if b.Command == "" {
		return nil
	}
	fmt.Fprintf(buildCommandOut, "› build      %s\n", b.Command)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", b.Command)
	} else {
		cmd = exec.Command("sh", "-c", b.Command)
	}
	cmd.Dir = b.Cwd
	cmd.Env = buildCommandEnv(b.Cwd)
	cmd.Stdout, cmd.Stderr = buildCommandOut, buildCommandOut
	if err := cmd.Run(); err != nil {
		return fail("build_command_failed", fmt.Sprintf("the wrangler config's build.command failed in %s: %s", b.Cwd, err), b.Command).
			withFix("its output is above; fix the build and re-run", "or run it yourself and remove \"build\" from "+a.WranglerFile)
	}
	return nil
}

// buildCommandEnv is the environment build.command runs in: every
// node_modules/.bin from cwd up to the root is put first on PATH, nearest
// first, as `npm run` does, so a locally installed tool (esbuild, tsc) is
// found.
func buildCommandEnv(cwd string) []string {
	var bins []string
	for d := cwd; ; d = filepath.Dir(d) {
		p := filepath.Join(d, "node_modules", ".bin")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			bins = append(bins, p)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	env := os.Environ()
	if len(bins) == 0 {
		return env
	}
	pre := strings.Join(bins, string(os.PathListSeparator))
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + pre + string(os.PathListSeparator) + v
			return env
		}
	}
	return append(env, "PATH="+pre)
}

// devBuild runs build.command for tw dev: once at start, then again only
// when a file under build.watch_dir changed since the last run. tw dev
// re-takes its change snapshot after a run, so the build's own output
// (often under assets.directory) is never an edit and can't loop.
type devBuild struct {
	watch string // snapshot of the watch dirs before the last run
}

// run runs the build if the watch dirs changed since the last run (or it
// never ran). ran says whether it ran; err is the build's failure.
func (d *devBuild) run(a *app) (ran bool, err error) {
	b := a.customBuild()
	if b.Command == "" {
		return false, nil
	}
	// Snapshot before the build: an edit made while it runs is seen next time.
	w := watchSnapshot(b.WatchDir)
	if d.watch == w {
		return false, nil
	}
	d.watch = w
	return true, a.runBuildCommand()
}

// watchSnapshot is snapshot over each watch dir.
func watchSnapshot(dirs []string) string {
	var s []string
	for _, d := range dirs {
		s = append(s, snapshot(d, ""))
	}
	return strings.Join(s, ",")
}

// printDevErr reports a tw dev reload's failure: an error event under
// --json, else the error text. tw dev keeps running.
func printDevErr(ev *events, err error) {
	if ev != nil {
		ev.error(err)
		return
	}
	printErr(err)
}
