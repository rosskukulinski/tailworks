package kvwire

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture returns testdata/formats/<name>. With TW_WRITE_FIXTURES=1 a
// missing fixture is written from gen first; an existing one is never
// overwritten (ADR-041 §4).
func fixture(t *testing.T, name string, gen func() []byte) []byte {
	t.Helper()
	p := filepath.Join("testdata", "formats", name)
	if os.Getenv("TW_WRITE_FIXTURES") == "1" {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, gen(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v (new fixture? run with TW_WRITE_FIXTURES=1)", p, err)
	}
	return b
}

// TestFormatFixtures: every committed KV entry decodes to what was written.
// hostd/gen's TestJSFormatFixtures decodes the same files with kv.js.
func TestFormatFixtures(t *testing.T) {
	put := fixture(t, "kv-entry-v1-put.bin", func() []byte {
		return Encode(Header{Key: "greeting", Expiration: 1893456000, Metadata: `{"lang":"en"}`}, []byte("hello"))
	})
	e, err := Decode(put)
	if err != nil {
		t.Fatal(err)
	}
	if e.Key != "greeting" || e.Expiration != 1893456000 || e.Metadata != `{"lang":"en"}` || e.Delete || string(e.Value) != "hello" {
		t.Fatalf("v1 put: %+v %q", e.Header, e.Value)
	}
	// Encode is still byte-identical for the same input (Go and JS agree).
	if !bytes.Equal(Encode(e.Header, e.Value), put) {
		t.Fatal("v1 put: re-encoding differs from the fixture")
	}
	del := fixture(t, "kv-entry-v1-del.bin", func() []byte { return Encode(Header{Key: "greeting", Delete: true}, nil) })
	if e, err := Decode(del); err != nil || !e.Delete || e.Key != "greeting" || len(e.Value) != 0 {
		t.Fatalf("v1 delete: %+v %v", e, err)
	}
	newer := append([]byte{2}, put[1:]...)
	if _, err := Decode(newer); err == nil || !strings.Contains(err.Error(), "newer tailworks") {
		t.Fatalf("v2 entry: %v", err)
	}
}
