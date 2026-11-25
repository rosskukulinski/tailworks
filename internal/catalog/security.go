package catalog

import "strings"

// HasSecurityNotes reports whether release notes rendered by Markdown (the
// GitHub release body release.yml publishes) have a Security section, that
// is, whether the release shipped a `security` fragment. The check is on
// the heading line itself (`## Security`), so a note that merely mentions
// security doesn't count.
func HasSecurityNotes(markdown string) bool {
	for _, line := range strings.Split(markdown, "\n") {
		if strings.TrimRight(line, " \t\r") == "## "+SecuritySection {
			return true
		}
	}
	return false
}
