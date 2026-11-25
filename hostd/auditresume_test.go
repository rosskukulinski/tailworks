package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// TestAuditResume (#377): the chain head is saved only when every chained
// record reached mgmt, is read once, and is used only when mgmt's head is
// the same hash and nothing was chained first.
func TestAuditResume(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	path := filepath.Join(t.TempDir(), auditHeadFile)
	ok := func(context.Context, []mgmtapi.AuditRecord) error { return nil }

	a := newAuditLog(key)
	a.add(mgmtapi.AuditRecord{Kind: "kill"})
	// Still queued: no head is saved, and a stale one is removed.
	os.WriteFile(path, []byte("stale\n"), 0o600)
	if err := a.saveHead(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("head saved with records still queued (%v)", err)
	}
	a.flush(context.Background(), ok)
	if err := a.saveHead(path); err != nil {
		t.Fatal(err)
	}
	head := a.prev

	// Next run: the file is consumed at load, whatever happens next.
	b := newAuditLog(key)
	b.loadResume(path, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("saved head left on disk after load (%v)", err)
	}
	if b.resume("other", nil) || b.prev != "" {
		t.Fatalf("continued from a head that is not mgmt's: prev %q", b.prev)
	}

	c := newAuditLog(key)
	c.resumeFrom = head
	c.add(mgmtapi.AuditRecord{Kind: "kill"}) // chained before registration
	if c.resume(head, nil) {
		t.Fatal("continued after a record already started a new segment")
	}

	d := newAuditLog(key)
	d.resumeFrom = head
	if !d.resume(head, nil) || d.prev != head {
		t.Fatalf("did not continue from the matching head: prev %q", d.prev)
	}
	if d.resume(head, nil) {
		t.Fatal("resume is one-shot")
	}
	d.add(mgmtapi.AuditRecord{Kind: "kill"})
	if d.pending[0].PrevHash != head {
		t.Fatalf("first record after resume links to %q, want %q", d.pending[0].PrevHash, head)
	}
}
