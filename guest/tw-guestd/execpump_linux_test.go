package main

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
)

// execPumpCollect runs execPumpOutput on r and returns what it sent.
func execPumpCollect(t *testing.T, r *os.File) []byte {
	t.Helper()
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		execPumpOutput(r, proto.FrameStdout, func(kind byte, p []byte) error {
			if kind != proto.FrameStdout {
				t.Errorf("frame kind %d", kind)
			}
			got.Write(p)
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("execPumpOutput did not return")
	}
	return got.Bytes()
}

// TestExecPumpDeadlineKeepsBufferedOutput is #611: the process has exited
// and its output is in the pipe, but the pump only gets to read after the
// post-exit deadline has passed (a loaded guest). The output must still
// reach the host. The write end stays open, as when the exec left a daemon
// behind, so the pump can't rely on EOF.
func TestExecPumpDeadlineKeepsBufferedOutput(t *testing.T) {
	for _, size := range []int{37, 32 << 10, 60 << 10} { // a boot_id line; one buffer; more than one
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Repeat([]byte("d51b7221-"), size/9+1)[:size]
		if _, err := w.Write(want); err != nil {
			t.Fatal(err)
		}
		r.SetReadDeadline(time.Now().Add(-time.Second))
		got := execPumpCollect(t, r)
		if !bytes.Equal(got, want) {
			t.Errorf("size %d: pump sent %d bytes, want %d", size, len(got), len(want))
		}
		r.Close()
		w.Close()
	}
}

// TestExecPumpDeadlineCutsOffDaemon: with nothing buffered and the write
// end held open, the deadline still ends the pump.
func TestExecPumpDeadlineCutsOffDaemon(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer r.Close()
	w.Write([]byte("before"))
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.SetReadDeadline(time.Now())
	}()
	if got := execPumpCollect(t, r); string(got) != "before" {
		t.Errorf("got %q, want %q", got, "before")
	}
}

// TestExecPumpEOF: the normal case reads to EOF.
func TestExecPumpEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Write([]byte("hello\n"))
	w.Close()
	if got := execPumpCollect(t, r); string(got) != "hello\n" {
		t.Errorf("got %q", got)
	}
}
