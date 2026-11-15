// Package twd holds the repo-root docs that `twd docs` serves next to
// docs/: README.md (with the quick start), CHANGELOG.md and SECURITY.md.
// go:embed can't reach a parent directory, so the docs package can't embed
// them itself; it reads them from RootDocs, as "../README.md" and so on.
package twd

import "embed"

// RootDocs is README.md, CHANGELOG.md and SECURITY.md.
//
//go:embed README.md CHANGELOG.md SECURITY.md
var RootDocs embed.FS

// RootDocNames lists RootDocs' files.
var RootDocNames = []string{
	"README.md",
	"CHANGELOG.md",
	"SECURITY.md",
}
