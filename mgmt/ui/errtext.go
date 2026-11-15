package ui

import (
	"fmt"
	"strings"
)

// errLines folds a deployment error for display: workerd's restart loop
// repeats the same line once per attempt ("Socket "ingress": bind(): …" ×5),
// which made a wall of red text. Consecutive repeats become one line with
// a count.
func errLines(s string) []string {
	var out []string
	n := 0
	flush := func() {
		if n > 1 {
			out[len(out)-1] += fmt.Sprintf("  (×%d)", n)
		}
	}
	prev := ""
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// "workerd exited 6 times: <line>" then <line> again: same line.
		if len(out) > 0 && (l == prev || strings.HasSuffix(prev, ": "+l)) {
			n++
			continue
		}
		flush()
		out = append(out, l)
		prev, n = l, 1
	}
	flush()
	return out
}

// errSummary is the first line of errLines, for a table cell.
func errSummary(s string) string {
	ls := errLines(s)
	if len(ls) == 0 {
		return ""
	}
	if len(ls) > 1 {
		return ls[0] + fmt.Sprintf(" (+%d more)", len(ls)-1)
	}
	return ls[0]
}
