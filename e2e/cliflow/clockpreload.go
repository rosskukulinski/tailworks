package cliflow

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// BuildClockPreload compiles internal/wallclock/testdata/skewclock.c, the
// preload library that lets a test build of tw-hostd skew its workerds'
// wall clock along with its own (TW_TEST_CLOCK_PRELOAD; build the binaries
// with BuildBinariesTags(t, "twtestclock")). It returns "" and why when it
// can't: the library is Linux only and needs a C compiler.
func BuildClockPreload(t testing.TB) (lib, why string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return "", "the clock preload library is Linux only"
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		return "", "no C compiler (cc)"
	}
	root, err := nettest.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	lib = filepath.Join(t.TempDir(), "skewclock.so")
	cmd := exec.Command(cc, "-shared", "-fPIC", "-O2", "-o", lib, filepath.Join(root, "internal", "wallclock", "testdata", "skewclock.c"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cc skewclock.c: %v\n%s", err, b)
	}
	return lib, ""
}
