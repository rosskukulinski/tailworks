package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Hints for running tailworks from a source checkout (issue #46): there the fix
// for missing JS packages is `make deps`, not "reinstall tw". A plain
// `make build` builds the Go binaries but doesn't vendor or build the JS
// packages, so bundling an app fails with `Could not resolve "@tailworks/…"`.

// twModule is the first line of a tailworks checkout's go.mod.
const twModule = "module github.com/rosskukulinski/tailworks"

// sourceCheckout is the root of the tailworks checkout this tailworks runs from (above
// the executable or the working directory), or "".
func sourceCheckout() string {
	for _, d := range repoRoots() { // state.go
		if !isFile(filepath.Join(d, "Makefile")) || !dirExists(filepath.Join(d, "js", "packages", "create-tailworks")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && strings.HasPrefix(string(b), twModule+"\n") {
			return d
		}
	}
	return ""
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// reinstallFix is the fix for missing or incomplete bundled files: `make
// deps` in a source checkout, else fallback (the installed release's fix).
func reinstallFix(fallback string) string {
	if root := sourceCheckout(); root != "" {
		return "cd " + root + " && make deps   (a source checkout: make build doesn't install or build the JS packages)"
	}
	return fallback
}

// noNodeModules is the doctor detail suffix when the bundler found no
// node_modules above create-tailworks (nodePaths returned none).
func noNodeModules(np []string, createTw string) string {
	if len(np) > 0 {
		return ""
	}
	return " (no node_modules found above " + createTw + ")"
}

// unresolvedTwImport reports whether bundler output says an @tailworks/*
// package (tailworks' own SDK) could not be resolved.
func unresolvedTwImport(out string) bool {
	return strings.Contains(out, `Could not resolve "@tailworks/`)
}

// withBundleFix adds the fix for an unresolved @tailworks/* import to a
// build_failed error: tailworks' JS packages are missing next to the bundler.
func withBundleFix(e *cliErr, out string) *cliErr {
	if !unresolvedTwImport(out) {
		return e
	}
	return e.withFix(reinstallFix("tailworks' JS packages (@tailworks/tailnet, @tailworks/mcp, …) are missing: reinstall tailworks (install.sh), then `tw doctor`"))
}
