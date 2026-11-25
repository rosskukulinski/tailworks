package catalog

import (
	"fmt"
	"slices"
	"strings"
)

// Changelog renders CHANGELOG.md from the release entries: newest release
// first, each with the same sections as its GitHub release notes
// (Markdown), one heading level down. `go run ./internal/catalog/changes
// changelog > CHANGELOG.md` writes it; scripts/release-notes.sh --finalize
// does so at release time, and TestChangelogUpToDate fails when it's stale.
func Changelog(es []Entry) string {
	es = slices.Clone(es)
	Sort(es)
	slices.Reverse(es)
	var b strings.Builder
	b.WriteString("# Changelog\n\n")
	b.WriteString("What changed in each tailworks release, newest first. This file is generated\n")
	b.WriteString("from the release catalog (`internal/catalog/releases/`, written from the\n")
	b.WriteString("fragments in `changes/released/`); don't edit it by hand. How versions\n")
	b.WriteString("work and what may break: [docs/RELEASING.md](docs/RELEASING.md#versions).\n")
	b.WriteString("What may run together, and how to upgrade:\n")
	b.WriteString("[docs/upgrading.md](docs/upgrading.md).\n")
	for _, e := range es {
		fmt.Fprintf(&b, "\n## v%s (%s)\n\n", e.Version, e.Date)
		if e.Workerd != "" {
			fmt.Fprintf(&b, "workerd %s.\n\n", e.Workerd)
		}
		md := Markdown("", e.Notes)
		for _, l := range strings.Split(strings.TrimRight(md, "\n"), "\n") {
			if strings.HasPrefix(l, "#") {
				l = "#" + l
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
