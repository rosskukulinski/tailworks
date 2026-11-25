package hostd_test

import (
	"fmt"
	"os"
)

// procEnv is pid's exec-time environment.
func procEnv(pid int) ([]byte, error) { return os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)) }
