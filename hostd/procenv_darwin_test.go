package hostd_test

import "golang.org/x/sys/unix"

// procEnv is pid's argv + exec-time environment, as any same-user process
// on macOS can read it (KERN_PROCARGS2).
func procEnv(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) }
