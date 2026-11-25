package hostd

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// procStartTime is the process's start time in clock ticks since boot
// (/proc/<pid>/stat field 22), as an opaque string.
func procStartTime(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // comm may contain spaces and parens
	if i < 0 {
		return "", errors.New("malformed stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 { // fields 3.. after comm: state is f[0], starttime is field 22 = f[19]
		return "", errors.New("malformed stat")
	}
	return f[19], nil
}
