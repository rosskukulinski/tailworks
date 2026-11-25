package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestFollowDevRecords: tw dev prints each request record (console lines,
// exception stack) as hostd appends it, and a "request" event with --json.
func TestFollowDevRecords(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "logs", "app.requests.jsonl") // created later, like hostd's
		var out syncBuf
		var ev *events
		if jsonMode {
			ev = newEvents(true, &out)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { followDevRecords(ctx, path, &out, ev); close(done) }()

		time.Sleep(3 * devRecordsEvery) // the file doesn't exist yet: no error, no output
		rec := obs.Record{Time: time.Now(), Kind: "request", Method: "GET", Path: "/boom", Status: 500, Outcome: "exception",
			Logs:       []obs.LogLine{{Level: "log", Message: "hit /boom"}},
			Exceptions: []obs.Exception{{Name: "Error", Message: "kaboom", Stack: "at fetch (index.js:1:2)"}}}
		b, _ := json.Marshal(rec)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), "kaboom") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		<-done
		got := out.String()
		for _, want := range []string{"/boom", "hit /boom", "kaboom", "index.js:1:2"} {
			if !strings.Contains(got, want) {
				t.Errorf("json=%v: output lacks %q:\n%s", jsonMode, want, got)
			}
		}
		if jsonMode && !strings.Contains(got, `"event":"request"`) {
			t.Errorf("json: no request event:\n%s", got)
		}
	}
}
