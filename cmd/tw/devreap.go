package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/internal/devreap"
)

// devTmpGlob matches tw dev's private dirs (dev.go: os.MkdirTemp("/tmp",
// "tw-dev-")).
const devTmpGlob = "/tmp/tw-dev-*"

// guardDevDir makes tw dev clean up even when it is SIGKILLed (issue #44):
// first it reaps the dirs of earlier tw dev runs whose process is gone
// (their workerd and node children, then the dir), then it guards tmp, so
// a watchdog kills this run's children and removes tmp however tw dev
// exits. Another live tw dev (another worktree's) is left alone. Failure
// is only a warning: tw dev still runs, it just can't promise the
// cleanup.
func guardDevDir(tmp, workerd string) {
	for _, d := range devreap.ReapStale(devTmpGlob) {
		fmt.Fprintf(os.Stderr, "tw dev: cleaned up %s (left by a tw dev that was killed)\n", filepath.Base(d))
	}
	if err := devreap.Guard(tmp, devreap.Options{Programs: []string{workerd}}); err != nil {
		fmt.Fprintf(os.Stderr, "tw dev: warning: no cleanup watchdog (%v); if tw dev is killed with SIGKILL, workerd may keep running\n", err)
	}
}
