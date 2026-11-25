package wallclock

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNowWithoutHook(t *testing.T) {
	if Offset() != 0 {
		t.Fatalf("offset %v in a build without the hook", Offset())
	}
	if d := time.Since(Now()); d < 0 || d > time.Second {
		t.Fatalf("Now is %v from time.Now", d)
	}
	if env, read := Workerd(); env != nil || read != nil {
		t.Fatalf("Workerd() = %v, %v without the hook", env, read)
	}
}

// TestFollow steps the offset file and checks that hostd's clock and
// the preload library's file follow it.
func TestFollow(t *testing.T) {
	defer func() { offset.Store(0); workerd = nil }()
	dir := t.TempDir()
	path := filepath.Join(dir, "clock")
	write := func(s string) {
		if err := writeAtomic(path, s); err != nil {
			t.Fatal(err)
		}
	}
	ft := func() string {
		b, _ := os.ReadFile(path + ".secs")
		return strings.TrimSpace(string(b))
	}
	write("5m")
	stop := make(chan struct{})
	defer close(stop)
	if err := follow(path, "/lib/skewclock.so", stop); err != nil {
		t.Fatal(err)
	}
	if Offset() != 5*time.Minute || ft() != "+300" {
		t.Fatalf("offset %v, secs file %q; want 5m, +300", Offset(), ft())
	}
	if d := time.Until(Now()); d < 5*time.Minute-time.Second || d > 5*time.Minute+time.Second {
		t.Fatalf("Now is %v ahead, want 5m", d)
	}
	env, read := Workerd()
	if !slices.Contains(env, "TW_TEST_CLOCK_FILE="+path+".secs") ||
		!slices.Contains(read, "/lib/skewclock.so") {
		t.Fatalf("Workerd() = %v, %v", env, read)
	}
	wait := func(want time.Duration, wantFT string) {
		t.Helper()
		for i := 0; Offset() != want || ft() != wantFT; i++ {
			if i > 50 {
				t.Fatalf("offset %v, secs file %q; want %v, %q", Offset(), ft(), want, wantFT)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	write("-3m30.4s") // rounded to the second, both sides
	wait(-210*time.Second, "-210")
	write("not a duration") // ignored: the last good offset stays
	time.Sleep(3 * pollEvery)
	wait(-210*time.Second, "-210")
	os.Remove(path)
	wait(0, "+0")
}

func TestReadOffsetBad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock")
	os.WriteFile(path, []byte("5 minutes"), 0o600)
	if err := follow(path, "", nil); err == nil {
		t.Fatal("follow accepted a bad offset file")
	}
}
