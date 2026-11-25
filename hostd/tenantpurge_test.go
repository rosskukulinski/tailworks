package hostd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/r2"
)

// TestPurgeDeletedTenant (issue #107): a tenant listed in the heartbeat
// reply's purgeTenants loses tenants/<id>/ (node state, DO storage, logs)
// and its R2 blobs on this host; another tenant keeps its own; the purge
// is reported until mgmt stops listing it; an unsafe ID is refused.
func TestPurgeDeletedTenant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &r2.FS{Root: filepath.Join(dir, "r2")}
	h := &Host{cfg: Config{DataDir: dir, R2: fs}, log: slog.New(slog.DiscardHandler), ctx: ctx,
		tenants: map[string]*tenant{}, workloads: map[string]*workload{}, objects: map[string]*object{}}
	files := func(id string) []string {
		return []string{
			filepath.Join(h.tenantDir(id), "nodes", "n1", "tailscaled.state"),
			filepath.Join(h.tenantDir(id), "workloads", "support", "do", "Room", "db.sqlite"),
			filepath.Join(h.tenantDir(id), "logs", "support.requests.jsonl"),
			filepath.Join(fs.Root, "t", id, "w", "support", "b", "media", "0123456789abcdef0123456789abcdef"),
		}
	}
	for _, id := range []string{"tn_gone", "tn_kept"} {
		for _, f := range files(id) {
			if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("data of "+id), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	h.purgeTenants([]string{"tn_gone", ".."})
	h.wg.Wait()
	for _, f := range files("tn_gone") {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("left after the purge: %s (%v)", f, err)
		}
	}
	if _, err := os.Stat(h.tenantDir("tn_gone")); !os.IsNotExist(err) {
		t.Errorf("tenant dir left: %v", err)
	}
	for _, f := range files("tn_kept") {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("another tenant's file went: %s (%v)", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "tenants")); err != nil {
		t.Errorf("the tenants dir went: %v", err)
	}
	if got := h.tenantPurge.done(); len(got) != 1 || got[0] != "tn_gone" {
		t.Fatalf("reported: %v", got)
	}
	// Listed again (the report was lost): purged again, still reported.
	h.purgeTenants([]string{"tn_gone"})
	h.wg.Wait()
	if got := h.tenantPurge.done(); len(got) != 1 {
		t.Fatalf("reported: %v", got)
	}
	// mgmt stops listing it: the report is dropped.
	h.purgeTenants(nil)
	if got := h.tenantPurge.done(); len(got) != 0 {
		t.Fatalf("reported after the ack: %v", got)
	}
}
