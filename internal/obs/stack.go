package obs

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// runtimeFrameRE is a stack frame in one of tailworks' own modules (the
// __tw_rt.js runtime, the generated __tw_main.js / __tw_facet.js
// entries): not the app's code.
var runtimeFrameRE = regexp.MustCompile(`(?:^|[\s(/])(__tw_[A-Za-z0-9_.-]*\.js):\d+:\d+\)?\s*$`)

// FoldRuntimeFrames folds each run of stack frames in tailworks' own modules
// into one line ("… 4 tailworks runtime frames (__tw_rt.js)"), so an uncaught
// exception shows the app's frames, as `wrangler dev` does. The JSON
// record keeps the whole stack.
func FoldRuntimeFrames(stack string) string {
	if !strings.Contains(stack, "__tw_") {
		return stack
	}
	lines := strings.Split(stack, "\n")
	out := make([]string, 0, len(lines))
	var run int
	mods := map[string]bool{}
	flush := func() {
		if run == 0 {
			return
		}
		names := make([]string, 0, len(mods))
		for m := range mods {
			names = append(names, m)
		}
		sort.Strings(names)
		s := "s"
		if run == 1 {
			s = ""
		}
		out = append(out, fmt.Sprintf("    … %d tw runtime frame%s (%s)", run, s, strings.Join(names, ", ")))
		run = 0
		clear(mods)
	}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "at ") {
			if m := runtimeFrameRE.FindStringSubmatch(t); m != nil {
				run++
				mods[path.Base(m[1])] = true
				continue
			}
		}
		flush()
		out = append(out, l)
	}
	flush()
	return strings.Join(out, "\n")
}
