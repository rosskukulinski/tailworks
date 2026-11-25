package main

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// execPumpOutput copies an exec's stdout or stderr pipe to the host as
// frames of the given kind until EOF, a send error, or the read deadline
// pumpExec sets once the process has exited.
//
// The deadline must not drop output (#611): Go's poller fails a read whose
// deadline has passed without trying the read at all, so a pump goroutine
// that was slow to be scheduled (a loaded guest just after boot) lost
// everything a short-lived process had already written, and the exec
// reported exit 0 with no output. So when the deadline hits, whatever is
// in the pipe is drained with non-blocking reads before giving up; only
// output still to come (a daemon the exec left behind holding the pipe)
// is cut off.
func execPumpOutput(f *os.File, kind byte, send func(byte, []byte) error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if send(kind, buf[:n]) != nil {
				return
			}
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			execDrainPipe(f, buf, kind, send)
			return
		}
		if err != nil {
			return
		}
	}
}

// execDrainPipe sends what is buffered in f now, without waiting for more.
func execDrainPipe(f *os.File, buf []byte, kind byte, send func(byte, []byte) error) {
	// The raw read below goes through the poller, which would refuse it
	// for the expired deadline too; it never blocks, so no deadline is
	// needed.
	if f.SetReadDeadline(time.Time{}) != nil {
		return
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return
	}
	rc.Read(func(fd uintptr) bool {
		for {
			n, err := unix.Read(int(fd), buf)
			if n <= 0 || err != nil {
				return true // drained (EAGAIN), EOF or an error
			}
			if send(kind, buf[:n]) != nil {
				return true
			}
		}
	})
}
