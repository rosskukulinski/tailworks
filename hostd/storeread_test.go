package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestStoreReadDeadline (#38): a platform-store read that wants fresher
// data than it has waits a bounded time (READ_WAIT_MS, 2 s) for mgmt's
// journal and then serves its local copy; while the journal stays
// unreachable later reads serve local data without waiting (the breaker),
// concurrent catch-ups coalesce, hostd gives up on a hung journal read
// (storeReadTimeout, 10 s), writes still fail, and the first catch-up that
// succeeds brings the store up to date again.
func TestStoreReadDeadline(t *testing.T) {
	t.Parallel()
	hostdtest.RegisterProbeStore()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("storeprobe"), "storeprobe", nil)
	wl := e.WaitActive("storeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	// all is the probe's read that always wants the journal's latest
	// (fresh(0)).
	all := func() (map[string]any, time.Duration, error) {
		t0 := time.Now()
		code, _, b := s.do("GET", "http://"+wl.IP+"/all", nil)
		el := time.Since(t0)
		var m map[string]any
		if code != 200 || json.Unmarshal(b, &m) != nil {
			return nil, el, fmt.Errorf("/all: %d %s", code, b)
		}
		return m, el, nil
	}
	if code, _, b := s.do("GET", "http://"+wl.IP+"/put?k=a&v=1", nil); code != 200 {
		t.Fatalf("put: %d %s", code, b)
	}
	if m, _, err := all(); err != nil || fmt.Sprint(m) != "map[a:1]" {
		t.Fatalf("all: %v %v", m, err)
	}

	// mgmt stops answering journal reads (slow or black-holed).
	e.FM.HoldStoreReads(true)
	reads0, _, _ := e.FM.StoreReadStats()
	var wg sync.WaitGroup
	errs := make([]error, 5)
	took := make([]time.Duration, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, el, err := all()
			if err == nil && fmt.Sprint(m) != "map[a:1]" {
				err = fmt.Errorf("served %v, want the local copy map[a:1]", m)
			}
			errs[i], took[i] = err, el
		}()
	}
	wg.Wait()
	t.Logf("reads with the journal hung took %v", took)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("read %d with the journal hung: %v", i, err)
		}
		if took[i] > 6*time.Second {
			t.Fatalf("read %d with the journal hung took %s, want about 2 s (READ_WAIT_MS)", i, took[i])
		}
	}
	// The breaker is open: the next read doesn't wait at all.
	if m, el, err := all(); err != nil || fmt.Sprint(m) != "map[a:1]" || el > time.Second {
		t.Fatalf("read with the breaker open: %v %v after %s, want map[a:1] at once", m, err, el)
	}
	// Six reads, one catch-up at mgmt: the rest coalesced into the one
	// queued behind it (which hasn't started).
	if reads, held, _ := e.FM.StoreReadStats(); reads-reads0 != 1 || held != 1 {
		t.Fatalf("journal reads at mgmt: %d (%d held), want 1: catch-ups should coalesce", reads-reads0, held)
	}
	// hostd gives up on the hung journal read (storeReadTimeout).
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, _, abandoned := e.FM.StoreReadStats(); abandoned >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hostd never gave up on the hung journal read")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// mgmt is unreachable: writes fail (journal first, RPO 0); reads
	// keep serving the local copy without waiting.
	e.FM.SetStoresDown(true)
	e.FM.HoldStoreReads(false)
	if code, _, b := s.do("GET", "http://"+wl.IP+"/put?k=b&v=2", nil); code != 500 || !strings.Contains(string(b), "store_unavailable") {
		t.Fatalf("write with the journal down: %d %s, want it refused (store_unavailable)", code, b)
	}
	if m, el, err := all(); err != nil || fmt.Sprint(m) != "map[a:1]" || el > time.Second {
		t.Fatalf("read with the journal down: %v %v after %s", m, err, el)
	}

	// mgmt is back, and another writer (an admin) appended meanwhile: a
	// background catch-up closes the breaker and the reads see it.
	e.FM.SetStoresDown(false)
	journal := "/v1/tenants/" + e.TenantID + "/workloads/storeprobe/stores/probe/main/journal"
	var page mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(context.Background(), "GET", journal, nil, &page); err != nil {
		t.Fatal(err)
	}
	var ar mgmtapi.StoreAppendResult
	if err := e.Mgmt.Do(context.Background(), "POST", journal, map[string]any{
		"after": page.Head, "incarnation": page.Incarnation,
		"entries": []any{map[string]any{"ckey": "c", "data": []byte(`{"k":"c","v":"from-admin"}`)}},
	}, &ar); err != nil {
		t.Fatalf("admin append: %v", err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		m, el, err := all()
		if err != nil || el > 3*time.Second {
			t.Fatalf("read after mgmt came back: %v %v after %s", m, err, el)
		}
		if fmt.Sprint(m) == "map[a:1 c:from-admin]" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store never caught up after mgmt came back: %v", m)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if code, _, b := s.do("GET", "http://"+wl.IP+"/put?k=b&v=2", nil); code != 200 {
		t.Fatalf("write after mgmt came back: %d %s", code, b)
	}
}
