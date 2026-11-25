package wallclock

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// The test hook (only a `-tags twtestclock` build turns it on: testhook_on.go).
//
//	TW_TEST_WALL_CLOCK_FILE=<path>   the offset, as a Go duration ("5m",
//	                                 "-3m30s", "0s"). Re-read every 100 ms,
//	                                 so a test steps the clock by rewriting
//	                                 it. A missing or empty file is 0.
//	TW_TEST_CLOCK_PRELOAD=<path>     a preload library that shifts a
//	                                 process's wall clock by the seconds in
//	                                 $TW_TEST_CLOCK_FILE (testdata/skewclock.c,
//	                                 built by e2e/cliflow.BuildClockPreload).
//	                                 When set, every workerd hostd starts
//	                                 runs with it, reading <path>.secs, so
//	                                 the whole host follows one offset.
//
// The offset is rounded to whole seconds, the same for hostd and workerd.
// On a step, <path>.secs is written first and hostd's own clock moves
// right after; workerd re-reads the file at most 100 ms later. Monotonic
// clocks are never shifted: a wrong wall clock doesn't make a machine's
// timers run fast or slow.

// pollEvery is how often follow re-reads the offset file.
const pollEvery = 100 * time.Millisecond

// follow sets the offset from path now and keeps it in step with the file
// until stop is closed. lib, if set, is the preload library: workerds get
// it, and the offset is mirrored to path+".secs" (written before hostd's
// own offset changes) for it to read.
func follow(path, lib string, stop <-chan struct{}) error {
	secs := path + ".secs"
	last := time.Duration(-1 << 63)
	apply := func() error {
		d, err := readOffset(path)
		if err != nil {
			return err
		}
		if d == last {
			return nil
		}
		if lib != "" {
			if err := writeAtomic(secs, secsSpec(d)); err != nil {
				return err
			}
		}
		offset.Store(int64(d))
		last = d
		return nil
	}
	if err := apply(); err != nil {
		return err
	}
	if lib != "" {
		env := []string{"TW_TEST_CLOCK_FILE=" + secs, "LD_PRELOAD=" + lib} // Linux only (skewclock.c)
		workerd = func() ([]string, []string) { return env, []string{lib, secs} }
	}
	go func() {
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				apply() // a half-written or bad file keeps the last offset
			}
		}
	}()
	return nil
}

// readOffset parses the offset file: a Go duration, rounded to seconds.
func readOffset(path string) (time.Duration, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("wall clock offset %q: %w", s, err)
	}
	return d.Round(time.Second), nil
}

// secsSpec is d as the preload library reads it: "+300", "-90".
func secsSpec(d time.Duration) string {
	return fmt.Sprintf("%+d\n", int64(d/time.Second))
}

func writeAtomic(path, s string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
