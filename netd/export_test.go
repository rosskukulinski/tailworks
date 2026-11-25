package netd

import "time"

// SetPublicRejectWindow shortens the reject-summary window for tests.
func SetPublicRejectWindow(d time.Duration) (restore func()) {
	old := publicRejectWindow
	publicRejectWindow = d
	return func() { publicRejectWindow = old }
}

// ErrNoTailnetCerts is the tailnet_https_unavailable refusal (tests inject
// it: testcontrol reports certificate domains it can't issue for).
var ErrNoTailnetCerts = errNoTailnetCerts

// CheckTailnetCerts is the default certificate source's status check.
var CheckTailnetCerts = checkTailnetCerts
