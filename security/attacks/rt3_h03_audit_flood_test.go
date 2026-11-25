package attacks

// RT3-03 / H03 regression test (High): a flood of tenant audit() notes (or,
// before the fix, of not_public requests) filled hostd's pending audit queue
// while mgmt was slow or away. hostd then trimmed the oldest records, which
// were already chained: the next flush started at a record whose prevHash
// mgmt had never seen, mgmt refused every batch from then on, and the
// host's chain stayed BrokenAt for good, host-source records included.
//
// Fixed invariant (hostd/audit.go): nothing chained is ever dropped. Host
// records spill to disk past the memory cap; sheddable records (tenant
// notes, and public_request once the spill is full) are refused before they
// are chained, and counted into an in-chain audit_dropped{n} record. Tenant
// notes also pass a per-object token bucket. Route refusals (not_public,
// bad_request) are rolled into the per-minute public_rejected summary
// (netd.TestPublicRoutes pins that part).
//
// This test drives hostd's real queue (hostd.AuditQueueForTest) into a real
// mgmt ingest (mgmt.IngestAudit) and verification.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

func TestRT3_H03_AuditFloodKeepsHostRecordsAndChain(t *testing.T) {
	ctx := context.Background()
	dir, err := mkShort()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(dir) })
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, Broker: &mgmt.Broker{BaseURL: "http://tsapi.invalid"},
		IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Hour, PolicyMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	if err := st.UpsertHost(ctx, mgmt.Host{ID: "h1", CapacityNodes: 10, Tiers: []string{"process"}, AuditPubKey: base64.StdEncoding.EncodeToString(pub)}); err != nil {
		t.Fatal(err)
	}

	// Small caps so the flood is quick: 50 records in memory, 64 KiB on disk.
	q, err := hostd.NewAuditQueueForTest(key, filepath.Join(dir, "audit-spill.jsonl"), 50, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	q.SetClock(func() time.Time { return t0 }) // the note buckets never refill during the flood

	mgmtUp := true
	ingest := func(ctx context.Context, recs []mgmtapi.AuditRecord) error {
		raws := make([]json.RawMessage, len(recs))
		for i, r := range recs {
			raws[i], _ = json.Marshal(r)
		}
		_, cs, err := m.IngestAudit(ctx, "h1", raws)
		if err != nil {
			return err
		}
		if !cs.OK {
			return fmt.Errorf("chain %s broken at %d: %s", cs.Chain, cs.BrokenAt, cs.Error)
		}
		return nil
	}
	send := func(ctx context.Context, recs []mgmtapi.AuditRecord) error {
		if !mgmtUp {
			return errors.New("mgmt unreachable")
		}
		return ingest(ctx, recs)
	}
	hostRec := func(kind, trace string) mgmtapi.AuditRecord {
		return mgmtapi.AuditRecord{Kind: kind, ObjectID: "obj-platform", Code: "probe", TraceID: trace,
			Detail: mgmtapi.Note("host record " + trace)}
	}

	// 0. A baseline record reaches mgmt.
	q.Host(hostRec("deny", "baseline"))
	if _, err := q.Flush(ctx, send); err != nil {
		t.Fatalf("baseline flush: %v", err)
	}

	// 1. The break mechanism: a batch whose first record is missing (what
	// trimming the queue's head did) is refused by mgmt, so a queue that
	// drops chained records can never flush again. The fixed queue keeps
	// the batch on a failed send, and the next flush links.
	q.Host(hostRec("kill", "gap-a"))
	q.Host(hostRec("kill", "gap-b"))
	if _, err := q.Flush(ctx, func(ctx context.Context, recs []mgmtapi.AuditRecord) error { return ingest(ctx, recs[1:]) }); err == nil {
		t.Fatal("mgmt accepted a batch with a hole in the chain; the H03 break mechanism is gone, rework this test")
	}
	if _, err := q.Flush(ctx, send); err != nil {
		t.Fatalf("flush after a refused batch: %v", err)
	}

	// 2. The flood, with mgmt away: tenant notes from five objects, the
	// internet's public_request records, and the platform's own decisions,
	// with a heartbeat flush (failing) every 500 steps.
	mgmtUp = false
	var wantHost []string // every host record that must reach mgmt
	shed, notesSent, notesTaken, pubSent := 0, 0, 0, 0
	for i := range 3000 {
		notesSent++
		if q.TenantNote(fmt.Sprintf("obj-tenant-%d", i%5), map[string]any{"kind": "allow", "detail": "flood"}) {
			notesTaken++
		} else {
			shed++
		}
		if i%3 == 0 {
			pubSent++
			tr := fmt.Sprintf("pub-%d", i)
			if q.Host(hostRec("public_request", tr)) {
				wantHost = append(wantHost, tr)
			} else {
				shed++
			}
		}
		if i%10 == 0 {
			tr := fmt.Sprintf("deny-%d", i)
			if !q.Host(hostRec("deny", tr)) {
				t.Fatalf("H03: host record %s was refused during the flood", tr)
			}
			wantHost = append(wantHost, tr)
		}
		if i%500 == 499 {
			if _, err := q.Flush(ctx, send); err == nil {
				t.Fatal("flush succeeded with mgmt away")
			}
		}
	}
	if shed == 0 || notesTaken == notesSent {
		t.Fatalf("the flood never reached the caps (shed %d, notes %d/%d): the test proves nothing", shed, notesTaken, notesSent)
	}
	if len(wantHost) <= 50 {
		t.Fatalf("only %d host records queued; the memory cap (50) was never passed", len(wantHost))
	}

	// 3. mgmt is back: one heartbeat drains everything and the chain verifies.
	mgmtUp = true
	if _, err := q.Flush(ctx, send); err != nil {
		t.Fatalf("H03: flush after the flood: %v", err)
	}
	if n := q.Queued(); n != 0 {
		t.Fatalf("%d records still queued after a successful flush", n)
	}
	if cs := m.VerifyChain(ctx, "host:h1"); !cs.OK {
		t.Fatalf("H03: host chain broken after the flood: %+v", cs)
	}

	recs, err := st.ListAudit(ctx, mgmt.AuditFilter{Chain: "host:h1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	dropped, notes := 0, 0
	reasons := map[string]int{}
	for _, r := range recs {
		switch r.Kind {
		case "audit_dropped":
			var d struct {
				N        int            `json:"n"`
				ByReason map[string]int `json:"byReason"`
			}
			var raw struct {
				Detail json.RawMessage `json:"detail"`
			}
			json.Unmarshal(r.Raw, &raw)
			if err := json.Unmarshal(raw.Detail, &d); err != nil {
				t.Fatalf("audit_dropped detail: %v (%s)", err, r.Raw)
			}
			dropped += d.N
			for k, v := range d.ByReason {
				reasons[k] += v
			}
		case "tenant_note":
			notes++
		default:
			got[r.TraceID] = true
		}
	}
	// 4. No host record lost.
	for _, tr := range wantHost {
		if !got[tr] {
			t.Fatalf("H03: host record %s was lost in the flood", tr)
		}
	}
	for _, tr := range []string{"baseline", "gap-a", "gap-b"} {
		if !got[tr] {
			t.Fatalf("host record %s missing", tr)
		}
	}
	// 5. The gap is visible and exact: every refused record is counted.
	if dropped != shed {
		t.Fatalf("audit_dropped counts %d records, %d were refused (%v)", dropped, shed, reasons)
	}
	if notes != notesTaken {
		t.Fatalf("mgmt holds %d tenant notes, hostd took %d", notes, notesTaken)
	}
	for _, r := range []string{"queue_full", "tenant_rate", "spill_full"} {
		if reasons[r] == 0 {
			t.Errorf("no %s drops counted (%v): the flood did not exercise that path", r, reasons)
		}
	}
	if reasons["spill_full"] >= pubSent {
		t.Errorf("every public_request was shed (%d of %d): the spill never held any", reasons["spill_full"], pubSent)
	}
	t.Logf("flood: %d host records kept, %d tenant notes kept of %d, %d refused %v", len(wantHost), notesTaken, notesSent, shed, reasons)
}
