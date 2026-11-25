package emailstub

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

// The built-in bundler refuses an import of `cloudflare:email` from the
// app's own code (docs/COMPAT.md "Email", docs/errors.md#not_supported):
// tailworks has no email module yet, and a Worker that imports it would
// only fail later, at `new EmailMessage()`. An import from a dependency
// under node_modules keeps the stub (ClassJS) instead, because the user
// can't remove it: the Agents SDK (`agents`) imports `cloudflare:email`
// at the top of its main module, so every Agents app would be refused.
//
// "The app's own code" is any importer whose real path has no
// node_modules directory in it. esbuild resolves symlinks, so a workspace
// or `npm link`ed package counts as the app's own code (the user can edit
// it), and a package installed under node_modules (the app's, or
// tailworks' own NodePaths) counts as a dependency. Pre-built (no_bundle,
// Vite-built) and --wrangler bundles are not bundled by tailworks, so the
// importer can't be told apart there: they get the workerd-config stub
// (hostd/gen, email.go) and are never refused.

// Code is the typed error code of the refusal.
const Code = "not_supported"

// Unsupported starts the refusal's message. build.mjs builds the same
// message (TestBuildMJSMatches).
const Unsupported = "cloudflare:email is not supported on tailworks yet"

// Refusal is the message for an import from importer (a path relative to
// the app directory).
func Refusal(importer string) string {
	return Unsupported + " (imported by " + importer + ")"
}

// Dependency reports whether importer (an absolute or relative path) is
// inside a node_modules directory.
func Dependency(importer string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(importer), "/"), "node_modules")
}

// ImportError is the bundler's refusal of the app's own imports of
// `cloudflare:email`.
type ImportError struct {
	Importers []string // relative to the app directory, sorted
}

func (e *ImportError) Error() string {
	return Refusal(strings.Join(e.Importers, ", "))
}

// AsImportError returns err's ImportError, if any.
func AsImportError(err error) (*ImportError, bool) {
	var ie *ImportError
	return ie, errors.As(err, &ie)
}
