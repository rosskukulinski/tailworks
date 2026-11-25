//go:build linux

package devreap

import (
	"os"
	"strconv"
)

// cwds returns each pid's working directory (where readable).
func cwds(pids []int) map[int]string {
	out := map[int]string{}
	for _, p := range pids {
		if d, err := os.Readlink("/proc/" + strconv.Itoa(p) + "/cwd"); err == nil {
			out[p] = d
		}
	}
	return out
}
