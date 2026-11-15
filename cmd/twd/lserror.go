package main

import (
	"fmt"
	"strings"
)

// deployErrorLine is a failed deployment's error on one line for `twd ls`:
// its first line (workerd repeats the same line once per restart), how many
// more there are, and where to read the rest.
func deployErrorLine(workload, err string) string {
	lines := strings.Split(strings.TrimSpace(err), "\n")
	first := strings.TrimSpace(lines[0])
	more := 0
	for _, l := range lines[1:] {
		if l = strings.TrimSpace(l); l != "" && l != first && !strings.HasSuffix(first, l) {
			more++
		}
	}
	const maxLen = 160
	if r := []rune(first); len(r) > maxLen {
		first = string(r[:maxLen]) + "…"
	}
	if more > 0 {
		first += fmt.Sprintf(" (+%d more lines)", more)
	}
	return first + " · twd logs " + workload + " --console"
}
