package hostd

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startGroup(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }() // reap it once killed, so alive() sees it gone
	t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-done })
	return cmd
}

// TestChildLedgerReapsSurvivors (#432): a child group recorded by one run
// and never removed (hostd crashed) is killed by the next run's
// openChildLedger; a recorded PID that now belongs to another process
// (start time differs) is left alone; a removed child is forgotten.
func TestChildLedgerReapsSurvivors(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := openChildLedger(dir, log)
	orphan, reused, done := startGroup(t), startGroup(t), startGroup(t)
	l.add(orphan.Process.Pid, "workerd a")
	l.add(done.Process.Pid, "workerd c")
	l.remove(done.Process.Pid)
	// reused: a ledger entry whose PID is alive but whose start time isn't
	// the one recorded (the PID was reused by an unrelated process).
	l.mu.Lock()
	l.recs[reused.Process.Pid] = childRec{PID: reused.Process.Pid, Start: "1.000000", What: "workerd b"}
	l.saveLocked()
	l.mu.Unlock()

	// "Crash": the ledger is left as is; a new run opens it.
	openChildLedger(dir, log)
	deadline := time.Now().Add(5 * time.Second)
	for alive(orphan.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("the orphaned child survived the next run's startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !alive(reused.Process.Pid) {
		t.Fatal("killed a process whose PID was only reused")
	}
	if !alive(done.Process.Pid) {
		t.Fatal("killed a child that had been removed from the ledger")
	}
	if _, err := os.Stat(filepath.Join(dir, childLedgerFile)); !os.IsNotExist(err) {
		t.Fatalf("ledger not cleared after the reap: %v", err)
	}
}

// TestChildLedgerTracksLive: add and remove keep the file in step with the
// live children.
func TestChildLedgerTracksLive(t *testing.T) {
	dir := t.TempDir()
	l := openChildLedger(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a, b := startGroup(t), startGroup(t)
	l.add(a.Process.Pid, "a")
	l.add(b.Process.Pid, "b")
	l.remove(a.Process.Pid)
	raw, err := os.ReadFile(filepath.Join(dir, childLedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	var recs []childRec
	json.Unmarshal(raw, &recs)
	if len(recs) != 1 || recs[0].PID != b.Process.Pid || recs[0].Start == "" {
		t.Fatalf("ledger: %s", raw)
	}
}
