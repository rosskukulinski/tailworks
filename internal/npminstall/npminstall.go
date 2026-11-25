// Package npminstall recognises a tailworks that npm installed (issue #240).
//
// The npm package `tailworks` is a small Node.js shim; the binaries come in
// one platform package per release target (@tailworks/cli-<os>-<arch>),
// which holds the same layout as an extracted release tarball (bin/,
// share/tailworks, VERSION) minus the microVM guest, plus its package.json.
// So tw finds its daemons, workerd and the bundler next to itself as in a
// tarball install, but the directory belongs to npm: tw upgrade and
// tw-hostd -self-upgrade must not swap it, and a login service must not
// point into npx's cache, which npm may clear at any time.
package npminstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// PackagePrefix starts every platform package's name.
const PackagePrefix = "@tailworks/cli-"

// UpgradeCommand upgrades a global npm install.
const UpgradeCommand = "npm install -g tailworks@latest"

// Install is a platform package tailworks runs from.
type Install struct {
	Dir     string // the package directory (above bin/)
	Package string // its name, e.g. @tailworks/cli-darwin-arm64
	Version string
	// NPX: the package is in npm exec's cache (~/.npm/_npx/<hash>/…),
	// not a global or project install.
	NPX bool
}

// At reports whether dir (an install root: the directory above bin/) is a
// platform package.
func At(dir string) (Install, bool) {
	if dir == "" {
		return Install{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return Install{}, false
	}
	var p struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &p) != nil || !strings.HasPrefix(p.Name, PackagePrefix) {
		return Install{}, false
	}
	return Install{Dir: dir, Package: p.Name, Version: p.Version, NPX: InNPXCache(dir)}, true
}

// Of reports whether the binary bin (symlinks resolved) is in a platform
// package.
func Of(bin string) (Install, bool) {
	if bin == "" {
		return Install{}, false
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	return At(filepath.Dir(filepath.Dir(bin)))
}

// Self is Of for the running executable.
func Self() (Install, bool) {
	exe, err := os.Executable()
	if err != nil {
		return Install{}, false
	}
	return Of(exe)
}

// InNPXCache reports whether path is in npm exec's (npx's) cache.
func InNPXCache(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/_npx/")
}

// Kind describes the install for messages: "npm" (a global or project
// install) or "npx (npm's cache)".
func (i Install) Kind() string {
	if i.NPX {
		return "npx (npm's cache)"
	}
	return "npm"
}
