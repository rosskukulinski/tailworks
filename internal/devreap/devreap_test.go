package devreap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the two processes the tests need:
//
//	DEVREAP_TEST_CHILD=1   a stand-in for workerd: sleeps until killed.
//	DEVREAP_TEST_OWNER=dir a stand-in for tw dev: Guards dir, starts a
//	                       child in its own process group (as hostd starts
//	                       workerd) whose command line names dir, prints
//	                       "ready <pid>" and waits to be killed.
func TestMain(m *testing.M) {
	if os.Getenv("DEVREAP_TEST_CHILD") != "" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if dir := os.Getenv("DEVREAP_TEST_OWNER"); dir != "" {
		os.Exit(ownerMain(dir))
	}
	os.Exit(m.Run())
}

func ownerMain(dir string) int {
	exe, _ := os.Executable()
	if err := Guard(dir, Options{Programs: []string{exe}}); err != nil {
		fmt.Println("guard:", err)
		return 1
	}
	c := startChild(exe, dir)
	if err := c.Start(); err != nil {
		fmt.Println("child:", err)
		return 1
	}
	fmt.Println("ready", c.Process.Pid)
	time.Sleep(time.Hour)
	return 0
}

func startChild(exe, dir string) *exec.Cmd {
	c := exec.Command(exe, "serve", filepath.Join(dir, "run", "config.capnp"))
	c.Env = append(os.Environ(), "DEVREAP_TEST_CHILD=1", "DEVREAP_TEST_OWNER=")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "tw-devreap-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func waitGone(t *testing.T, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(dl) {
			t.Fatalf("%s: still there after 15s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The issue #44 repro: the owner is SIGKILLed; its watchdog kills the
// child and removes the dir.
func TestGuardSIGKILL(t *testing.T) {
	dir := shortDir(t)
	exe, _ := os.Executable()
	owner := exec.Command(exe)
	owner.Env = append(os.Environ(), "DEVREAP_TEST_OWNER="+dir)
	out, _ := owner.StdoutPipe()
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "ready ") {
		owner.Process.Kill()
		t.Fatalf("owner: %q %v", line, err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "ready ")))
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) })
	if got := Processes(dir, exe); len(got) != 1 || got[0] != child {
		t.Fatalf("Processes = %v, want [%d]", got, child)
	}

	owner.Process.Signal(syscall.SIGKILL)
	owner.Wait()
	waitGone(t, "child", func() bool { return !alive(child) })
	waitGone(t, "dir", func() bool { _, err := os.Stat(dir); return os.IsNotExist(err) })
}

// Guard records this process as the owner, with the options.
func TestGuardKeep(t *testing.T) {
	dir := shortDir(t)
	if err := Guard(dir, Options{Keep: true}); err != nil {
		t.Fatal(err)
	}
	o, err := readOwner(dir)
	if err != nil || o.PID != os.Getpid() || !o.Keep {
		t.Fatalf("owner = %+v, %v", o, err)
	}
}

func writeOwner(t *testing.T, dir string, o owner) {
	b, _ := json.Marshal(o)
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func deadPID(t *testing.T) int {
	c := exec.Command("/bin/sh", "-c", "exit 0")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// ReapStale: a dir whose owner is dead is reaped; one whose owner is alive
// (another worktree's tw dev) and one with no owner file are not; and a
// process that isn't one of the dir's programs is never signalled, even if
// its command line names the dir.
func TestReapStale(t *testing.T) {
	exe, _ := os.Executable()
	root := shortDir(t)
	stale, live, legacy := filepath.Join(root, "x-stale"), filepath.Join(root, "x-live"), filepath.Join(root, "x-legacy")
	for _, d := range []string{stale, live, legacy} {
		os.MkdirAll(filepath.Join(d, "run"), 0o700)
	}
	writeOwner(t, stale, owner{PID: deadPID(t), Programs: []string{exe}})
	writeOwner(t, live, owner{PID: os.Getppid(), Programs: []string{exe}}) // go test: alive

	start := func(c *exec.Cmd) *exec.Cmd {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Process.Kill() })
		return c
	}
	staleChild := start(startChild(exe, stale))
	liveChild := start(startChild(exe, live))
	legacyChild := start(startChild(exe, legacy))
	bystander := start(exec.Command("/bin/sh", "-c", "sleep 60; :", filepath.Join(stale, "run", "workerd.log")))
	time.Sleep(200 * time.Millisecond) // let ps see them

	done := make(chan error, 1)
	go func() { done <- staleChild.Wait() }() // reap the zombie as soon as it dies
	got := ReapStale(filepath.Join(root, "x-*"))
	if len(got) != 1 || got[0] != stale {
		t.Fatalf("ReapStale = %v, want [%s]", got, stale)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stale dir's child still running")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dir not removed: %v", err)
	}
	for name, c := range map[string]*exec.Cmd{"live": liveChild, "legacy": legacyChild, "bystander": bystander} {
		if err := c.Process.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("%s process was killed: %v", name, err)
		}
	}
	for _, d := range []string{live, legacy} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed: %v", d, err)
		}
	}
}
