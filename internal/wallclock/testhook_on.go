//go:build twtestclock

package wallclock

import (
	"fmt"
	"os"
)

// A test build reads the hook's environment (testhook.go). A production
// build doesn't include this file, so nothing can shift its clock.
func init() {
	path := os.Getenv("TW_TEST_WALL_CLOCK_FILE")
	if path == "" {
		return
	}
	if err := follow(path, os.Getenv("TW_TEST_CLOCK_PRELOAD"), nil); err != nil {
		fmt.Fprintf(os.Stderr, "TEST HOOK: TW_TEST_WALL_CLOCK_FILE: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "TEST HOOK: wall clock follows %s (offset now %v, workerd skewed too: %v)\n", path, Offset(), workerd != nil)
}
