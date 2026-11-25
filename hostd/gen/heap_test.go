package gen_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// heapFiles is a Worker whose /grow allocates and holds about 1 GiB, and
// whose other paths answer small.
func heapFiles() map[string][]byte {
	const js = `export default { fetch(req) {
  if (new URL(req.url).pathname !== "/grow") return Response.json({ ok: true });
  const keep = [];
  for (let i = 0; i < 2048; i++) keep.push(new Array(1 << 16).fill(i)); // 2048 × 512 KiB
  return Response.json({ held: keep.length });
} };
`
	return map[string][]byte{
		"wrangler.jsonc":  []byte(`{"name": "heap", "main": "index.js", "compatibility_date": "2026-04-01"}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"tags": ["tag:ttest-heap"]}}`),
		"index.js":        []byte(js),
	}
}

// TestGeneratedHeapCap (#115): Options.HeapMiB becomes workerd's
// --max-old-space-size. A Worker allocating past it can't take the host's
// memory with it: V8 aborts this workerd ("Reached heap limit"), which
// hostd then restarts like any crash (TestWorkerdCrashRecovery). Without
// the cap the same request completes.
func TestGeneratedHeapCap(t *testing.T) {
	in := startFilesOpts(t, heapFiles(), func(o *gen.Options) { o.HeapMiB = 64 })
	cfg, _ := os.ReadFile(in.cfg)
	if !strings.Contains(string(cfg), `v8Flags = [ "--max-old-space-size=64" ]`) {
		t.Fatalf("config.capnp has no heap flag:\n%s", cfg)
	}
	if code, _, body := in.get("/", wl); code != 200 {
		t.Fatalf("GET /: %d %v", code, body)
	}
	if code, _, body := in.get("/grow", wl); code == 200 {
		t.Fatalf("GET /grow under a 64 MiB heap succeeded: %v", body)
	}
	done := make(chan error, 1)
	go func() { done <- in.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("workerd still running after it passed its heap limit")
	}
	if b, _ := os.ReadFile(in.log); !strings.Contains(string(b), "heap limit") && !strings.Contains(string(b), "heap out of memory") {
		t.Fatalf("workerd log has no heap-limit abort:\n%s", b)
	}

	t.Run("Uncapped", func(t *testing.T) {
		in := startFiles(t, heapFiles())
		cfg, _ := os.ReadFile(in.cfg)
		if strings.Contains(string(cfg), "v8Flags") {
			t.Fatalf("HeapMiB 0 still sets v8Flags:\n%s", cfg)
		}
		if code, _, body := in.get("/grow", wl); code != 200 || body["held"] != float64(2048) {
			t.Fatalf("GET /grow without a cap: %d %v", code, body)
		}
	})
}
