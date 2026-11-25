package hostd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// objectsV0 is objects.json as hostd wrote it before versioning.
const objectsV0 = `[
 {
  "id": "obj_1",
  "netdID": "n1",
  "tenantID": "t1",
  "workload": "chat",
  "class": "Room",
  "name": "lobby",
  "hostname": "chat-room-lobby",
  "tags": [
   "tag:chat"
  ],
  "status": "ready",
  "nodeID": "nNODE1",
  "wakes": 3,
  "created": "2026-11-15T12:00:00Z"
 },
 {
  "id": "obj_2",
  "netdID": "n2",
  "tenantID": "t1",
  "workload": "chat",
  "name": "chat",
  "hostname": "chat",
  "tags": null,
  "status": "killed",
  "wakes": 0,
  "created": "2026-11-15T12:00:01Z"
 }
]`

func TestObjectsFormatFixtures(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	v1 := func() []byte {
		b, _ := json.MarshalIndent([]*object{
			{V: objectsVersion, ID: "obj_1", NetdID: "n1", TenantID: "t1", Workload: "chat", Class: "Room", Name: "lobby",
				Hostname: "chat-room-lobby", Tags: []string{"tag:chat"}, Status: statusReady, NodeID: "nNODE1", Wakes: 3, Created: created},
			{V: objectsVersion, ID: "obj_2", NetdID: "n2", TenantID: "t1", Workload: "chat", Name: "chat",
				Hostname: "chat", Status: statusKilled, Created: created.Add(time.Second)},
		}, "", " ")
		return b
	}
	for _, name := range []string{"objects-v0.json", "objects-v1.json"} {
		gen := v1
		if name == "objects-v0.json" {
			gen = func() []byte { return []byte(objectsV0) }
		}
		list, err := decodeObjects(formatFixture(t, name, gen))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(list) != 2 {
			t.Fatalf("%s: %d records", name, len(list))
		}
		a, b := list[0], list[1]
		if a.ID != "obj_1" || a.Class != "Room" || a.Name != "lobby" || a.NodeID != "nNODE1" || a.Wakes != 3 || !a.Created.Equal(created) ||
			len(a.Tags) != 1 || a.Status != statusReady {
			t.Errorf("%s: record 0 = %+v", name, a)
		}
		if b.ID != "obj_2" || b.Status != statusKilled || b.Class != "" {
			t.Errorf("%s: record 1 = %+v", name, b)
		}
	}
	if _, err := decodeObjects([]byte(`[{"v":2,"id":"x"}]`)); err == nil || !strings.Contains(err.Error(), "newer tailworks") {
		t.Fatalf("v2 record: %v", err)
	}
}

// TestSaveObjectsWritesVersion: saveObjects stamps every record.
func TestSaveObjectsWritesVersion(t *testing.T) {
	t.Parallel()
	h := &Host{cfg: Config{DataDir: t.TempDir()}, objects: map[string]*object{"obj_1": {ID: "obj_1"}}}
	h.saveObjects()
	b, err := os.ReadFile(h.objectsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"v": 1`) {
		t.Fatalf("objects.json: %s", b)
	}
}
