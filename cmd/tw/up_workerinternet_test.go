package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/hostdrun"
)

// TestUpWorkerInternet (#295 item 5): tw up --worker-internet is saved
// in tw.json, kept by a later tw up (a restart, or tw upgrade's
// tw up --yes) unless given again, and reaches the host in both layouts;
// direct by default, including for state written before it existed.
func TestUpWorkerInternet(t *testing.T) {
	var old Config
	if old.workerInternet() != "direct" || old.workerInternetHostdArgs() != nil {
		t.Fatalf("old state: %q %v", old.workerInternet(), old.workerInternetHostdArgs())
	}
	var ho hostdrun.Options
	old.workerInternetSingle(&ho)
	if ho.WorkerInternet != "direct" {
		t.Fatalf("old state, single process: %q", ho.WorkerInternet)
	}
	if fs := upFlagSet(t); fs.Lookup("worker-internet").Value.String() != "direct" {
		t.Fatalf("default --worker-internet = %q", fs.Lookup("worker-internet").Value.String())
	}

	var c Config
	for _, bad := range []string{"", "on", "exit-node", "exit-node="} {
		if err := saveWorkerInternet(&c, bad); err == nil || !strings.Contains(err.Error(), "want direct") {
			t.Errorf("--worker-internet %q: %v", bad, err)
		}
	}
	if err := saveWorkerInternet(&c, "exit-node=x,dns=nope"); err == nil || !strings.Contains(err.Error(), "want an IP") {
		t.Errorf("--worker-internet exit-node=x,dns=nope: %v", err)
	}
	// An exit node (ADR-054) keeps the node's case, and is the
	// containers' path too, in both layouts.
	if err := saveWorkerInternet(&c, "Exit-Node=nABC,dns=1.1.1.1"); err != nil || c.WorkerInternet != "exit-node=nABC,dns=1.1.1.1" {
		t.Fatalf("exit-node saved: %q %v", c.WorkerInternet, err)
	}
	if a := c.workerInternetHostdArgs(); !slices.Equal(a, []string{"-worker-internet", "exit-node=nABC,dns=1.1.1.1", "-container-internet", "exit-node"}) {
		t.Fatalf("exit-node hostd args: %v", a)
	}
	c.workerInternetSingle(&ho)
	if ho.WorkerInternet != "exit-node=nABC,dns=1.1.1.1" || ho.ContainerInternet != "exit-node" {
		t.Fatalf("exit-node single process: %q %q", ho.WorkerInternet, ho.ContainerInternet)
	}
	ho = hostdrun.Options{}
	if err := saveWorkerInternet(&c, "OFF"); err != nil || c.WorkerInternet != "off" {
		t.Fatalf("saved: %q %v", c.WorkerInternet, err)
	}
	if a := c.workerInternetHostdArgs(); !slices.Equal(a, []string{"-worker-internet", "off"}) {
		t.Fatalf("hostd args: %v", a)
	}
	c.workerInternetSingle(&ho)
	if ho.WorkerInternet != "off" {
		t.Fatalf("single process: %q", ho.WorkerInternet)
	}

	// A later tw up keeps it; given again, the flag wins.
	fs := upFlagSet(t)
	keepSavedUpFlags(fs, c)
	if got := fs.Lookup("worker-internet").Value.String(); got != "off" {
		t.Fatalf("--worker-internet = %q, want the saved off", got)
	}
	fs = upFlagSet(t)
	if err := fs.Parse([]string{"--worker-internet=direct"}); err != nil {
		t.Fatal(err)
	}
	keepSavedUpFlags(fs, c)
	if got := fs.Lookup("worker-internet").Value.String(); got != "direct" {
		t.Fatalf("--worker-internet = %q, want the given direct", got)
	}
}

func TestWorkerInternetDetail(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		saved, running, problem, prefix string
		restart                         bool
	}{
		{"direct", "", "", "direct (Workers' fetch() reaches public", false},
		{"off", "off", "", "off (Workers' fetch() to internet hosts is refused", false},
		// The host still runs what it started with: say so.
		{"off", "direct", "", "direct (", true},
		{"exit-node=exit-fra,dns=1.1.1.1", "exit-node=exit-fra,dns=1.1.1.1", "", "exit-node=exit-fra,dns=1.1.1.1 (Workers' and containers' internet traffic leaves through tailnet exit node exit-fra via", false},
		{"exit-node=exit-fra", "exit-node=exit-fra", "exit-fra is offline", "exit-node=exit-fra (", false},
	} {
		d := workerInternetDetail(c.saved, c.running, c.problem)
		if c.problem != "" && !strings.Contains(d, "unavailable: "+c.problem) {
			t.Errorf("%s: %q lacks the problem", c.saved, d)
		}
		if !strings.HasPrefix(d, c.prefix) {
			t.Errorf("%s/%s: %q, want prefix %q", c.saved, c.running, d, c.prefix)
		}
		if got := strings.Contains(d, "tw.json says "+c.saved+": restart"); got != c.restart {
			t.Errorf("%s/%s: %q: restart hint %v, want %v", c.saved, c.running, d, got, c.restart)
		}
	}
	// tw doctor's one-line summary shows the mode.
	if f := metaOf("worker_internet").fact(workerInternetDetail("off", "", "")); f != "off" {
		t.Errorf("doctor fact = %q, want off", f)
	}
}

func TestDoctorWorkerInternet(t *testing.T) {
	t.Parallel()
	s := &state{Dir: t.TempDir(), Cfg: Config{Tailnet: "t.ts.net", WorkerInternet: "off"}}
	if st, d, _ := doctorWorkerInternet(s); st != "info" || !strings.HasPrefix(d, "off (") {
		t.Fatalf("off: %s %q", st, d)
	}
	s.Cfg.WorkerInternet = ""
	if st, d, _ := doctorWorkerInternet(s); st != "ok" || !strings.HasPrefix(d, "direct (") {
		t.Fatalf("default: %s %q", st, d)
	}
	s.Cfg.WorkerInternet = "exit-node=exit-fra"
	if st, d, _ := doctorWorkerInternet(s); st != "ok" || !strings.HasPrefix(d, "exit-node=exit-fra (") || !strings.Contains(d, "--worker-internet=direct") {
		t.Fatalf("exit-node (host not running): %s %q", st, d)
	}
	if st, _, _ := doctorWorkerInternet(&state{Dir: t.TempDir()}); st != "skip" {
		t.Fatalf("not set up: %s", st)
	}
}
