package obs

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRotatingDropBefore (#120): records older than the cutoff go from
// both files; the file keeps taking writes after; a line without a time
// stops the cut; a predecessor with nothing new left is removed.
func TestRotatingDropBefore(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/w.requests.jsonl"
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	line := func(i int) string {
		return fmt.Sprintf(`{"time":%q,"kind":"request","n":%d}`+"\n", base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339Nano), i)
	}
	var prev strings.Builder
	for i := range 3 {
		prev.WriteString(line(i))
	}
	os.WriteFile(path+".1", []byte(prev.String()), 0o600)
	r, err := OpenRotating(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 3; i < 8; i++ {
		r.Write([]byte(line(i)))
	}
	n, err := r.DropBefore(base.Add(5*time.Hour), RecordTime)
	if err != nil || n != 5 {
		t.Fatalf("dropped %d, %v", n, err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("old predecessor kept: %v", err)
	}
	r.Write([]byte(line(8)))
	b, _ := os.ReadFile(path)
	if got := strings.Count(string(b), "\n"); got != 4 || !strings.HasPrefix(string(b), line(5)) || !strings.HasSuffix(string(b), line(8)) {
		t.Fatalf("after the cut:\n%s", b)
	}
	// A line without a time stops the cut.
	r.Write([]byte("not json\n"))
	r.Write([]byte(line(9)))
	if n, err := r.DropBefore(base.Add(100*time.Hour), RecordTime); err != nil || n != 4 {
		t.Fatalf("second cut: %d %v", n, err)
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), "not json\n") {
		t.Fatalf("cut past an unreadable line:\n%s", b)
	}
	if n, err := DropBeforeFile(dir+"/missing", base, RecordTime); n != 0 || err != nil {
		t.Fatalf("missing file: %d %v", n, err)
	}
}
