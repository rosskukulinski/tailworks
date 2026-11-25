package hostd

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// auditLog is this host's hash-chained audit log (contract POST /v1/audit):
// each record carries prevHash/hash (mgmtapi.ChainHash) and hostSig
// (ed25519 with the host's audit key, registered as auditPubKey). Pending
// records are flushed to mgmt with every heartbeat; on failure they stay
// queued.
//
// Flood rules (RT3-03 / H03). A chained record is never removed from the
// queue except by a successful flush: dropping one would leave a gap mgmt
// can never link across, so the chain would stay broken for good.
//   - Host-source records are never dropped. Past maxPendingAudit in
//     memory they spill to disk (auditSpill), in order.
//   - Records that may be shed (tenant notes; public_request records once
//     the spill is full) are refused before they are chained, never after,
//     and counted.
//   - Tenant notes also pass a per-object token bucket (auditlimit.go).
//   - Whatever was refused is reported at the next flush as one in-chain
//     audit_dropped{n} record, so the chain verifies and the gap is visible.
type auditLog struct {
	mu      sync.Mutex
	key     ed25519.PrivateKey
	prev    string
	pending []mgmtapi.AuditRecord // in memory, oldest first; always older than the spill
	spill   *auditSpill           // nil: no data dir (unit tests), memory only
	tail    []mgmtapi.AuditRecord // newer than the spill, kept while the disk refuses writes
	drops   auditDrops            // refused since the last audit_dropped record
	notes   tenantNoteLimiter
	flushMu sync.Mutex // one flush at a time
	now     func() time.Time
	memMax  int // maxPendingAudit; lowered by tests

	// resumeFrom: the head a clean shutdown saved, until the first
	// registration (auditresume.go).
	resumeFrom string
}

// maxPendingAudit is how many records wait in memory before host records
// spill to disk and tenant notes are refused.
const maxPendingAudit = 10000

// auditFlushBatch caps one POST /v1/audit (mgmt reads at most 64 MiB).
const auditFlushBatch = 1000

func newAuditLog(key ed25519.PrivateKey) *auditLog {
	return &auditLog{key: key, now: time.Now, notes: newTenantNoteLimiter(), memMax: maxPendingAudit}
}

// Audit sources (RT-02). Every record this host signs says who produced
// its content; the field is part of the hashed body.
const (
	sourceHost   = "host"
	sourceTenant = "tenant"
	// kindTenantNote is the only kind tenant code can produce.
	kindTenantNote = "tenant_note"
	// kindAuditDropped reports records refused before they were chained.
	kindAuditDropped = "audit_dropped"
	// kindPublicRequest is one per public request (D7): internet-driven,
	// so the one host kind shed once the spill is full.
	kindPublicRequest = "public_request"
)

// Reasons in an audit_dropped record's detail.
const (
	dropQueueFull  = "queue_full"  // tenant note while mgmt is behind
	dropTenantRate = "tenant_rate" // tenant note over the per-object bucket
	dropSpillFull  = "spill_full"  // public_request with the spill at its cap
	dropRestart    = "restart"     // spilled records left by a previous run
)

type auditDrops struct {
	n        int
	byReason map[string]int
	from     time.Time
}

func (d *auditDrops) count(reason string, n int, now time.Time) {
	if n <= 0 {
		return
	}
	if d.n == 0 {
		d.from = now
		d.byReason = map[string]int{}
	}
	d.n += n
	d.byReason[reason] += n
}

// add chains r, or refuses it (returning false) when it may be shed. A
// host-source record is always chained.
func (a *auditLog) add(r mgmtapi.AuditRecord) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if r.TS == "" {
		r.TS = now.UTC().Format(time.RFC3339Nano)
	}
	if r.Source == "" {
		r.Source = sourceHost
	}
	if r.Source == sourceTenant && r.Kind != kindTenantNote {
		r.Kind = kindTenantNote // defence in depth: never a privileged kind from tenant content
	}
	if r.Source == sourceTenant {
		if !a.notes.allow(r.ObjectID, now) {
			a.drops.count(dropTenantRate, 1, now)
			return false
		}
		if a.backedUp() {
			a.drops.count(dropQueueFull, 1, now)
			return false
		}
	} else if r.Kind == kindPublicRequest && a.spill != nil && a.spill.full() {
		a.drops.count(dropSpillFull, 1, now)
		return false
	}
	a.chain(r)
	return true
}

// backedUp: memory is full or older records are on disk. a.mu held.
func (a *auditLog) backedUp() bool {
	return len(a.pending) >= a.memMax || len(a.tail) > 0 || (a.spill != nil && a.spill.count > 0)
}

// chain links, signs and queues r. a.mu held. The queue is three
// segments, oldest first: pending (memory), the spill (disk), and tail
// (memory, only while the disk refuses writes). A record goes to the
// newest non-empty segment, so the queue stays in chain order.
func (a *auditLog) chain(r mgmtapi.AuditRecord) {
	r.PrevHash = a.prev
	r.Hash = mgmtapi.ChainHash(a.prev, r)
	r.HostSig = mgmtapi.HostSig(a.key, r.Hash)
	a.prev = r.Hash
	switch {
	case len(a.tail) > 0:
		a.tail = append(a.tail, r)
	case a.spill != nil && a.backedUp():
		if err := a.spill.append(r); err != nil {
			// The disk refused (full, read-only): keep it in memory rather
			// than lose it.
			a.tail = append(a.tail, r)
		}
	default:
		a.pending = append(a.pending, r)
	}
}

// noteDrops chains one audit_dropped record for everything refused since
// the last one. a.mu held.
func (a *auditLog) noteDrops() {
	d := a.drops
	if d.n == 0 {
		return
	}
	a.drops = auditDrops{}
	now := a.now()
	b, _ := json.Marshal(map[string]any{"n": d.n, "byReason": d.byReason,
		"from": d.from.UTC().Format(time.RFC3339Nano), "to": now.UTC().Format(time.RFC3339Nano)})
	a.chain(mgmtapi.AuditRecord{TS: now.UTC().Format(time.RFC3339Nano), Kind: kindAuditDropped, Source: sourceHost, Detail: b, TraceID: newTraceID()})
}

// next returns the oldest queued records (at most auditFlushBatch) and a
// commit that removes them once mgmt has them.
func (a *auditLog) next() ([]mgmtapi.AuditRecord, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) == 0 && (a.spill == nil || a.spill.count == 0) && len(a.tail) > 0 {
		a.pending, a.tail = a.tail, nil
	}
	if len(a.pending) > 0 {
		n := min(len(a.pending), auditFlushBatch)
		recs := append([]mgmtapi.AuditRecord(nil), a.pending[:n]...)
		return recs, func() {
			a.mu.Lock()
			a.pending = a.pending[n:] // chain only appends, so these are still the first n
			if len(a.pending) == 0 {
				a.pending = nil
			}
			a.mu.Unlock()
		}, nil
	}
	if a.spill == nil || a.spill.count == 0 {
		return nil, nil, nil
	}
	recs, off, err := a.spill.read(auditFlushBatch)
	if err != nil || len(recs) == 0 {
		return nil, nil, err
	}
	return recs, func() {
		a.mu.Lock()
		a.spill.commit(off, len(recs))
		a.mu.Unlock()
	}, nil
}

// flush sends every queued record, oldest first, in batches, stopping at
// the first error (the rest stays queued, in order, for the next flush).
func (a *auditLog) flush(ctx context.Context, send func(context.Context, []mgmtapi.AuditRecord) error) (int, error) {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	a.mu.Lock()
	a.noteDrops()
	a.mu.Unlock()
	sent := 0
	for {
		recs, commit, err := a.next()
		if err != nil || len(recs) == 0 {
			return sent, err
		}
		if err := send(ctx, recs); err != nil {
			return sent, err
		}
		commit()
		sent += len(recs)
	}
}

func (h *Host) flushAudit(ctx context.Context) {
	if _, err := h.audit.flush(ctx, h.mgmt.Audit); err != nil {
		h.log.Warn("audit flush failed (will retry)", "queued", h.audit.queued(), "err", err)
	}
}

// queued is how many chained records wait for mgmt.
func (a *auditLog) queued() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.pending) + len(a.tail)
	if a.spill != nil {
		n += a.spill.count
	}
	return n
}
