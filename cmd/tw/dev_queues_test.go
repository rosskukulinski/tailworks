package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevQueues: `tw dev` runs a Worker's queue producers and consumers
// locally (ADR-039): a send through env.JOBS on the dev port reaches
// the app's own queue() handler, through the dev's fake mgmt.
func TestDevQueues(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	// --port 0 and the ready event's url, not a port picked and closed
	// here: a parallel test can take a closed port before tw dev binds it.
	work := t.TempDir()
	cmd := exec.Command(filepath.Join(bin, "tw"), "dev", hostdtest.Fixture("queues"), "--port", "0", "--json", "--state-dir", filepath.Join(work, "state"))
	cmd.Env = append(os.Environ(), "TW_WORKERD="+wd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuilder
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
		}
	})
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		sent := false
		for sc.Scan() {
			out.Write(append(sc.Bytes(), '\n'))
			var m struct{ Event, URL string }
			if !sent && json.Unmarshal(sc.Bytes(), &m) == nil && m.Event == "ready" && m.URL != "" {
				ready <- m.URL
				sent = true
			}
		}
		close(ready)
	}()
	var base string
	select {
	case u, ok := <-ready:
		if !ok {
			t.Fatalf("tw dev exited before it was ready:\n%s", out.String())
		}
		base = strings.TrimSuffix(u, "/")
	case <-time.After(90 * time.Second):
		t.Fatalf("tw dev did not serve the app:\n%s", out.String())
	}
	get := func(path string) (int, string) {
		r, err := http.Get(base + path)
		if err != nil {
			return 0, err.Error()
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _ := get("/env"); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tw dev was ready but did not serve /env:\n%s", out.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
	if code, body := get("/send?k=dev1"); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, body := get("/log")
		var l struct{ Recs []struct{ K string } }
		json.Unmarshal([]byte(body), &l)
		if len(l.Recs) > 0 && l.Recs[0].K == "dev1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the consumer never ran under tw dev: %s\n%s", body, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// syncBuilder is a strings.Builder safe for the stderr copier and the
// stdout scanner to write while the test reads it.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
