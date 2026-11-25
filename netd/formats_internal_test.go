package netd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// formatFixture returns testdata/formats/<name>; TW_WRITE_FIXTURES=1
// writes it from gen only if it is missing (ADR-041 §4).
func formatFixture(t *testing.T, name string, gen func() []byte) []byte {
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

// TestObjectMetaFormatFixtures: every committed object.json still reads.
func TestObjectMetaFormatFixtures(t *testing.T) {
	gens := map[string]func() []byte{
		// object.json as netd wrote it before versioning.
		"object-v0.json": func() []byte {
			return []byte(`{"id":"n1","hostname":"chat-room-lobby","tags":["tag:chat"],"ephemeral":false,"public":{"enabled":true}}`)
		},
		"object-v1.json": func() []byte {
			b, _ := json.Marshal(objectMeta{V: ObjectMetaVersion, ID: "n1", Hostname: "chat-room-lobby", Tags: []string{"tag:chat"},
				Public: &PublicOptions{Enabled: true}})
			return b
		},
	}
	for _, name := range []string{"object-v0.json", "object-v1.json"} {
		m, err := decodeMeta(formatFixture(t, name, gens[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.ID != "n1" || m.Hostname != "chat-room-lobby" || len(m.Tags) != 1 || m.Ephemeral || m.Public == nil {
			t.Errorf("%s: %+v", name, m)
		}
	}
	if _, err := decodeMeta([]byte(`{"v":2,"id":"n1"}`)); err == nil || !strings.Contains(err.Error(), "newer tailworks") {
		t.Fatalf("v2: %v", err)
	}
}
