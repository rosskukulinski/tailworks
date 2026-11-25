package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// collect is a flush target that keeps what it was sent.
type collect struct{ recs []mgmtapi.AuditRecord }

func (c *collect) send(_ context.Context, recs []mgmtapi.AuditRecord) error {
	c.recs = append(c.recs, recs...)
	return nil
}

func newTestAudit(t *testing.T, memMax int) (*auditLog, string) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	a := newAuditLog(key)
	a.memMax = memMax
	p := filepath.Join(t.TempDir(), "audit-spill.jsonl")
	if err := a.openSpill(p, nil); err != nil {
		t.Fatal(err)
	}
	return a, p
}

func mustLink(t *testing.T, recs []mgmtapi.AuditRecord) {
	t.Helper()
	if ok, problems := mgmtapi.VerifyChain(recs); !ok {
		t.Fatalf("chain: %v", problems)
	}
}

// H03: past the memory cap host records go to disk and come back in chain
// order, in batches no larger than auditFlushBatch.
func TestAuditSpillKeepsOrder(t *testing.T) {
	t.Parallel()
	a, p := newTestAudit(t, 10)
	for i := range 2500 {
		a.add(mgmtapi.AuditRecord{Kind: "deny", TraceID: string(rune('a' + i%26))})
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		t.Fatalf("nothing spilled: %v %v", fi, err)
	}
	var c collect
	batches := 0
	n, err := a.flush(context.Background(), func(ctx context.Context, recs []mgmtapi.AuditRecord) error {
		if len(recs) > auditFlushBatch {
			t.Fatalf("batch of %d", len(recs))
		}
		batches++
		return c.send(ctx, recs)
	})
	if err != nil || n != 2500 || len(c.recs) != 2500 || batches < 3 {
		t.Fatalf("flush: %d %v (%d recs, %d batches)", n, err, len(c.recs), batches)
	}
	mustLink(t, c.recs)
	if fi, _ := os.Stat(p); fi.Size() != 0 {
		t.Fatalf("spill not truncated after a full flush: %d bytes", fi.Size())
	}
	// The next record links to the last one sent.
	a.add(mgmtapi.AuditRecord{Kind: "kill"})
	a.flush(context.Background(), c.send)
	mustLink(t, c.recs)
}

// A failed send leaves the batch queued; nothing is lost or reordered.
func TestAuditFlushFailureKeepsBatch(t *testing.T) {
	t.Parallel()
	a, _ := newTestAudit(t, 5)
	for range 20 {
		a.add(mgmtapi.AuditRecord{Kind: "deny"})
	}
	calls := 0
	var landed collect
	_, err := a.flush(context.Background(), func(ctx context.Context, recs []mgmtapi.AuditRecord) error {
		calls++
		if calls == 2 { // the first batch (memory) lands, the spill read fails to send
			return errors.New("down")
		}
		return landed.send(ctx, recs)
	})
	if err == nil || a.queued() != 15 {
		t.Fatalf("after a failed send: err %v, queued %d (want 15)", err, a.queued())
	}
	var c collect
	if _, err := a.flush(context.Background(), c.send); err != nil || len(c.recs) != 15 {
		t.Fatalf("retry: %v %d", err, len(c.recs))
	}
	mustLink(t, append(landed.recs, c.recs...))
}

// Spilled records of an earlier run can't link to this run's chain: they
// are discarded and reported as audit_dropped{restart}.
func TestAuditSpillAfterRestart(t *testing.T) {
	t.Parallel()
	a, p := newTestAudit(t, 1)
	for range 5 {
		a.add(mgmtapi.AuditRecord{Kind: "deny"})
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	b := newAuditLog(key)
	if err := b.openSpill(p, nil); err != nil {
		t.Fatal(err)
	}
	var c collect
	b.flush(context.Background(), c.send)
	if len(c.recs) != 1 || c.recs[0].Kind != kindAuditDropped || c.recs[0].PrevHash != "" {
		t.Fatalf("after restart: %+v", c.recs)
	}
	var d struct {
		N        int            `json:"n"`
		ByReason map[string]int `json:"byReason"`
	}
	json.Unmarshal(c.recs[0].Detail, &d)
	if d.N != 4 || d.ByReason[dropRestart] != 4 {
		t.Fatalf("audit_dropped %s, want n=4 restart=4", c.recs[0].Detail)
	}
}

// Tenant notes: a per-object bucket, refused before chaining, counted.
func TestTenantNoteBucket(t *testing.T) {
	t.Parallel()
	a, _ := newTestAudit(t, 100000)
	now := time.Unix(1_700_000_000, 0)
	a.now = func() time.Time { return now }
	took := 0
	for range tenantNoteBurst + 50 {
		if a.add(TenantAuditRecord("o1", "t", "", map[string]any{"detail": "x"})) {
			took++
		}
	}
	if took != tenantNoteBurst {
		t.Fatalf("o1 took %d notes in a burst, want %d", took, tenantNoteBurst)
	}
	if !a.add(TenantAuditRecord("o2", "t", "", nil)) {
		t.Fatal("o2 shares o1's bucket")
	}
	now = now.Add(time.Second)
	if !a.add(TenantAuditRecord("o1", "t", "", nil)) {
		t.Fatal("o1's bucket did not refill")
	}
	var c collect
	a.flush(context.Background(), c.send)
	mustLink(t, c.recs)
	if last := c.recs[len(c.recs)-1]; last.Kind != kindAuditDropped || !json.Valid(last.Detail) {
		t.Fatalf("last flushed record %q, want the audit_dropped chained at flush", last.Kind)
	}
}
