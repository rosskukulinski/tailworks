package hostd

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// AuditQueueForTest is hostd's real audit queue without a Host, for the
// flood regression tests outside the package (security/attacks, RT3-03).
type AuditQueueForTest struct{ a *auditLog }

// NewAuditQueueForTest opens a queue that signs with key and spills to
// spillPath ("" = memory only). memMax (records) and spillMax (bytes)
// lower the memory and spill caps when > 0.
func NewAuditQueueForTest(key ed25519.PrivateKey, spillPath string, memMax int, spillMax int64) (*AuditQueueForTest, error) {
	a := newAuditLog(key)
	if memMax > 0 {
		a.memMax = memMax
	}
	if spillPath != "" {
		if err := a.openSpill(spillPath, nil); err != nil {
			return nil, err
		}
		if spillMax > 0 {
			a.spill.max = spillMax
		}
	}
	return &AuditQueueForTest{a: a}, nil
}

// Host adds a host-source record (as netd/hostd do).
func (q *AuditQueueForTest) Host(r mgmtapi.AuditRecord) bool { return q.a.add(r) }

// TenantNote adds a note as the host API's audit op does.
func (q *AuditQueueForTest) TenantNote(objectID string, note map[string]any) bool {
	return q.a.add(TenantAuditRecord(objectID, newTraceID(), "", note))
}

// Flush sends the queue, oldest first, as the heartbeat does.
func (q *AuditQueueForTest) Flush(ctx context.Context, send func(context.Context, []mgmtapi.AuditRecord) error) (int, error) {
	return q.a.flush(ctx, send)
}

// Queued is how many chained records wait.
func (q *AuditQueueForTest) Queued() int { return q.a.queued() }

// SetClock replaces the queue's clock (the tenant note bucket refills by it).
func (q *AuditQueueForTest) SetClock(now func() time.Time) {
	q.a.mu.Lock()
	q.a.now = now
	q.a.mu.Unlock()
}
