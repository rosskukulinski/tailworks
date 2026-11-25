package hostd

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// procStartTime is the process's start time (kern.proc.pid), as an opaque
// string.
func procStartTime(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if kp.Proc.P_pid != int32(pid) {
		return "", fmt.Errorf("pid %d: no such process", pid)
	}
	t := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), nil
}
