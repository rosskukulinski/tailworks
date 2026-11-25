package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// TestRecordLogMaxAge (#120): with RecordMaxAge the host drops request
// records older than it, from an open workload log and from the log of a
// workload that no longer runs here; newer records and later writes stay.
func TestRecordLogMaxAge(t *testing.T) {
	t.Parallel()
	h, w := telHost(t, TelemetryOptions{RecordRate: 1e6, RecordBurst: 1e6, RecordMaxAge: time.Hour})
	now := time.Now()
	for _, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, 30 * time.Minute, time.Minute} {
		h.tel.emit(w, &obs.Record{Time: now.Add(-age), Kind: obs.KindRequest, Path: "/x"})
	}
	gone := filepath.Join(h.tenantDir("tnB"), "logs", "old.requests.jsonl")
	os.MkdirAll(filepath.Dir(gone), 0o700)
	os.WriteFile(gone, []byte(`{"time":"`+now.Add(-5*time.Hour).UTC().Format(time.RFC3339Nano)+`","kind":"request"}`+"\n"), 0o600)

	if n := h.tel.pruneRecords(now); n != 3 {
		t.Fatalf("pruned %d records, want 3", n)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("an old log with nothing new is kept: %v", err)
	}
	h.tel.emit(w, &obs.Record{Time: now, Kind: obs.KindRequest, Path: "/y"})
	b, _ := os.ReadFile(h.RequestLogPath("tnA", "web"))
	if got := strings.Count(string(b), "\n"); got != 3 || !strings.Contains(string(b), `"/y"`) {
		t.Fatalf("log after the prune (%d lines):\n%s", got, b)
	}
}
