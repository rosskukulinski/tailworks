// Package wallclock is the wall clock tw-hostd reads for decisions that
// compare its time with another process's: tickets and caller seals it
// mints for its workerds, the timestamps on their egress and connect MACs
// it verifies, and when a cron trigger is due. Durations (time.Since, the
// fence, timeouts) use Go's monotonic clock and don't go through here.
//
// In a production build Now is time.Now. A test build (`go build -tags
// twtestclock`) can shift it at run time, to simulate a host whose clock is
// wrong or steps: see testhook.go. Nothing in a production build reads the
// test hook's environment variables.
package wallclock

import (
	"sync/atomic"
	"time"
)

var offset atomic.Int64 // nanoseconds; only a test build sets it

// Now is the wall clock as this host reads it.
func Now() time.Time {
	if o := offset.Load(); o != 0 {
		return time.Now().Add(time.Duration(o))
	}
	return time.Now()
}

// Offset is how far Now is from the real clock (0 outside a test build).
func Offset() time.Duration { return time.Duration(offset.Load()) }

// workerd, set only by a test build, is the extra environment for a
// workerd process and the files it must be able to read (so a sandbox
// lets it).
var workerd func() (env, read []string)

// Workerd returns the extra environment a workerd needs to see the same
// skewed clock as hostd, and the paths it must read for it. Both are nil
// outside a test build, or when the test didn't ask for workerd's clock to
// be skewed too.
func Workerd() (env, read []string) {
	if workerd == nil {
		return nil, nil
	}
	return workerd()
}
