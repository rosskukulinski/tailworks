package hostd

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/r2"
)

// TestR2BlobPrefixIsTheReplicaGroup pins hostd's half of #348 (ledger
// H-r2group) through the handler the platform calls (r2API): a replica's
// blobs go under its workload's prefix, the prefix its stores are scoped to
// in mgmt, and any other workload's go under its own name. hostd reads the
// name with internal/replicaname, as mgmt does when it refuses a workload
// that is not a replica such a name (mgmt.TestRT_Hr2group_ReplicaNameReserved).
func TestR2BlobPrefixIsTheReplicaGroup(t *testing.T) {
	for name, want := range map[string]string{
		"web":            "web",
		"web--r2":        "web",
		"web--r3":        "web",
		"web--r16":       "web",
		"api--r10":       "api",
		"web--preview-x": "web--preview-x",
		// Not replica names: nothing is stripped.
		"web--r1":  "web--r1",
		"web--r02": "web--r02",
		"web--rx":  "web--rx",
		"web-r2":   "web-r2",
	} {
		if got := r2Group(name); got != want {
			t.Errorf("r2Group(%q) = %q, want %q", name, got, want)
		}
	}

	dir := t.TempDir()
	h := &Host{cfg: Config{R2: &r2.FS{Root: dir}}, log: slog.New(slog.DiscardHandler)}
	instOf := func(name string) *instance {
		return &instance{w: &workload{h: h, tenantID: "tn1", name: name},
			app: &bundle.App{Stores: []bundle.StoreBinding{{Kind: "r2", Name: "media"}}}}
	}
	put := func(name string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.r2API(instOf(name), rec, httptest.NewRequest("PUT", "/v1/r2/blob?bucket=media", strings.NewReader("bytes of "+name)))
		var out struct{ ID string }
		if json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != 200 || !r2.ValidID(out.ID) {
			t.Fatalf("%s: PUT: %d %s", name, rec.Code, rec.Body.String())
		}
		return out.ID
	}
	at := func(group, id string) string { return filepath.Join(dir, "t", "tn1", "w", group, "b", "media", id) }
	for name, group := range map[string]string{"web": "web", "web--r2": "web", "shop": "shop"} {
		id := put(name)
		if b, err := os.ReadFile(at(group, id)); err != nil || string(b) != "bytes of "+name {
			t.Fatalf("%s: its blob is not under %s's prefix: %q %v", name, group, b, err)
		}
	}
	// A replica reads what its workload wrote (one bucket, as on Cloudflare
	// a Worker's instances share its buckets).
	id := put("web")
	rec := httptest.NewRecorder()
	h.r2API(instOf("web--r2"), rec, httptest.NewRequest("GET", "/v1/r2/blob?bucket=media&id="+id, nil))
	if rec.Code != 200 || rec.Body.String() != "bytes of web" {
		t.Fatalf("web--r2 reading web's blob: %d %q", rec.Code, rec.Body.String())
	}
}
