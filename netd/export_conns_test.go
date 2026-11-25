package netd

import "time"

// SetPublicFirstRequestTimeout shortens the first-request deadline for tests.
func SetPublicFirstRequestTimeout(d time.Duration) (restore func()) {
	old := publicFirstRequestTimeout
	publicFirstRequestTimeout = d
	return func() { publicFirstRequestTimeout = old }
}
