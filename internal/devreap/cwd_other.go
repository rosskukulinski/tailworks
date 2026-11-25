//go:build !linux

package devreap

import (
	"os/exec"
	"strconv"
	"strings"
)

// cwds returns each pid's working directory, from lsof (macOS has no
// /proc; lsof reads it with proc_pidinfo).
func cwds(pids []int) map[int]string {
	out := map[int]string{}
	if len(pids) == 0 {
		return out
	}
	ps := make([]string, len(pids))
	for i, p := range pids {
		ps[i] = strconv.Itoa(p)
	}
	b, _ := exec.Command("lsof", "-a", "-d", "cwd", "-n", "-P", "-Fn", "-p", strings.Join(ps, ",")).Output()
	pid := 0
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n") && pid != 0:
			out[pid] = l[1:]
		}
	}
	return out
}
