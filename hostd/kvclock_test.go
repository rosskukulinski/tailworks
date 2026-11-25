package hostd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestKVTTLAfterClockStepBack is #568 (found by e2e/dataplane's skew
// fault): a key put with expirationTtl must be readable right after the
// host's wall clock steps back.
//
// A Durable Object's Date.now() never goes back (workerd holds it at the
// latest value it returned), while a stateless isolate's follows the
// clock. KV's backend turned the TTL into an absolute expiry on its own
// clock and the store object compared it with the object's, so after a
// step back larger than the TTL every new TTL key was born expired.
//
// workerd runs with internal/wallclock's preload library, which shifts
// its wall clock (Linux, needs cc).
func TestKVTTLAfterClockStepBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the clock preload library is Linux only")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	real, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	lib, clock := filepath.Join(dir, "skewclock.so"), filepath.Join(dir, "clock")
	if b, err := exec.Command(cc, "-shared", "-fPIC", "-O2", "-o", lib, filepath.Join("..", "internal", "wallclock", "testdata", "skewclock.c")).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, b)
	}
	step := func(secs string) {
		t.Helper()
		if err := os.WriteFile(clock, []byte(secs), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond) // the library re-reads it every 100 ms
	}
	step("+0")
	wrap := filepath.Join(dir, "workerd")
	script := "#!/bin/sh\nexport LD_PRELOAD=" + lib + " TW_TEST_CLOCK_FILE=" + clock + "\nexec " + real + ` "$@"` + "\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
		func(o *hostdtest.Options) { o.Workerd = wrap })
	dep := s.e.Deploy(hostdtest.Fixture("kv"), "kv", nil)
	wl := s.e.WaitActive("kv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	c := &kvCaller{t: t, s: s, ip: wl.IP}
	const W = "/op"

	// The store object reads a clock 5 min ahead, then the host's clock
	// steps back: the object's stays ahead, the backend's doesn't.
	step("+300")
	c.must(W, "KV", "get", "warm")
	step("+0")
	c.must(W, "KV", "put", "ttl/step", "v", map[string]any{"expirationTtl": 60})
	eq(t, "a 60 s TTL key just after a 5 min step back", c.must(W, "KV", "get", "ttl/step"), "v")
	l := c.must(W, "KV", "list", map[string]any{"prefix": "ttl/"}).(map[string]any)
	if keys, _ := l["keys"].([]any); len(keys) != 1 {
		t.Fatalf("list ttl/ after the step back: %v", l)
	}
}
