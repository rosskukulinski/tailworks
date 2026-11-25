package main

// Security releases in the update check (issue #338). A release whose notes
// have a Security section (scripts/release-notes.sh renders one for every
// `security` fragment; catalog.HasSecurityNotes reads it back from the
// GitHub release body) is marked in `tw version --check`
// (securityRelease) and in the once-a-day notice that follows commands such
// as tw doctor and tw status, so an operator sees that the upgrade is
// urgent, not routine.

// securityReleaseTag is the marker appended to "tw X is out (this is Y)".
func securityReleaseTag(security bool) string {
	if !security {
		return ""
	}
	return " — security release, upgrade soon"
}
