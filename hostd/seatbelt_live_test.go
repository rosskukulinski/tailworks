package hostd_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
)

// seatbeltLiveEscapes runs the issue #121 probes under a running
// workload's live Seatbelt profile: a TCP connect to a loopback port it
// wasn't given, a signal to tw-hostd (this test process), a write to /tmp
// and a read of a file there (issue #175) must all fail; a write to its own
// instance dir must work.
func seatbeltLiveEscapes(t *testing.T, profile string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	tmpFile := fmt.Sprintf("/tmp/tw-seatbelt-live-%d", os.Getpid())
	defer os.Remove(tmpFile)
	readable := fmt.Sprintf("/tmp/tw-seatbelt-live-read-%d", os.Getpid())
	if err := os.WriteFile(readable, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(readable)
	probe := func(args ...string) error {
		cmd, err := hostd.SandboxCommand(profile, args...)
		if err != nil {
			t.Fatal(err)
		}
		return cmd.Run()
	}
	for name, args := range map[string][]string{
		"tcp_connect_loopback": {"/usr/bin/nc", "-z", "-w", "3", "127.0.0.1", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)},
		"signal_hostd":         {"/bin/kill", "-0", strconv.Itoa(os.Getpid())},
		"write_tmp":            {"/bin/sh", "-c", "echo x > " + tmpFile},
		"read_tmp":             {"/bin/cat", readable},
	} {
		if err := probe(args...); err == nil {
			t.Fatalf("seatbelt: %s allowed under the workload's profile %s", name, profile)
		}
	}
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatalf("seatbelt: %s written under the workload's profile", tmpFile)
	}
	own := filepath.Join(filepath.Dir(profile), "probe-own")
	if err := probe("/bin/sh", "-c", "echo x > "+own); err != nil {
		t.Fatalf("seatbelt: the workload can't write its own instance dir: %v", err)
	}
	os.Remove(own)
}
