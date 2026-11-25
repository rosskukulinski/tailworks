package main_test

import (
	"os"
	"testing"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestMain sets TW_WORKERD once for the whole test binary (package main and
// main_test). The tests that spawn tw all need the same workerd, and a test
// that calls t.Setenv can't run in parallel.
func TestMain(m *testing.M) {
	if os.Getenv("TW_WORKERD") == "" {
		if wd, err := nettest.WorkerdBinary(); err == nil {
			os.Setenv("TW_WORKERD", wd)
		}
	}
	os.Exit(m.Run())
}
