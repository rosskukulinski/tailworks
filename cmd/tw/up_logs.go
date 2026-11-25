package main

import (
	"io"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// daemonLogMax is where tw up's mgmt.log and hostd.log rotate (issue
// #120): the file is renamed to <name>.log.1, replacing the one before,
// so at most twice this is on disk per daemon. supervisor.log is written
// only at start and stop and isn't rotated.
const daemonLogMax = 32 << 20

// openDaemonLog opens a daemon's log under <state>/logs for appending.
func openDaemonLog(path string) (io.WriteCloser, error) {
	return obs.OpenRotating(path, daemonLogMax)
}
