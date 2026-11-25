package main

import (
	"context"
	"sync"

	"github.com/rosskukulinski/tailworks/netd"
)

// How `tw __supervise` exits (#110). A clean stop (a shutdown signal:
// `tw down`, `systemctl --user stop`, `launchctl bootout`) exits 0. A
// component that stops on its own (single process: mgmt's listener failed;
// two processes: a child that keeps exiting) exits 1, so the service
// manager restarts tw: the systemd unit has Restart=on-failure and the
// LaunchAgent KeepAlive{SuccessfulExit=false} (service.go,
// service_launchd.go). Both only restart on a non-zero exit.

// supStop is closed once, by a shutdown signal (err nil) or by a component
// that gave up (err set). err is safe to read after done is closed.
type supStop struct {
	once sync.Once
	done chan struct{}
	err  error
}

func newSupStop() *supStop { return &supStop{done: make(chan struct{})} }

// stop closes done; only the first call's err counts.
func (s *supStop) stop(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
	})
}

// gaveUpErr is the separate-host supervisor's exit error when child c
// exited too often to restart it again.
func gaveUpErr(c *child) error {
	code := "mgmt_failed"
	if c.name == "tw-hostd" {
		code = "host_failed"
	}
	return fail(code, c.name+" keeps exiting; the supervisor gave up and exits 1 so the service manager restarts it",
		"log: "+c.logPath)
}

// waitSingle blocks the single-process supervisor until ctx is done (a
// shutdown signal: nil, exit 0) or mgmt reports a failed listener (an
// error, exit 1). A listener error that races a shutdown signal counts as
// the clean stop.
func waitSingle(ctx context.Context, mgmtErrs <-chan error, logf func(string, ...any)) error {
	select {
	case <-ctx.Done():
		logf("stopping: host first (nodes close in parallel), then mgmt (flushes pending device deletes)")
		return nil
	case err := <-mgmtErrs:
		if ctx.Err() != nil {
			logf("stopping: host first (nodes close in parallel), then mgmt (flushes pending device deletes)")
			return nil
		}
		logf("mgmt listener failed: %v; stopping (exit 1: the service manager restarts tw)", err)
		return fail("mgmt_failed", "the embedded tw-mgmt stopped: its listener failed", netd.Redact(err.Error())).
			withFix("tw logs --component mgmt", "under tw up --service it restarts on its own; otherwise tw up")
	}
}
