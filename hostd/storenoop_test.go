package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestStoreNoopWriteChecksJournal (#562): a write that the local copy says
// changes nothing (a delete of a key it doesn't have) is decided on a
// copy caught up with the journal, not acknowledged from a stale one. The
// R2 chaos loop (e2e/dataplane, r2-replicas) saw a replica acknowledge a
// delete of a key the other replica had just put, and the key stay.
func TestStoreNoopWriteChecksJournal(t *testing.T) {
	t.Parallel()
	hostdtest.RegisterProbeStore()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("storeprobe"), "storeprobe", nil)
	wl := e.WaitActive("storeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	call := func(path string) (int, string) {
		code, _, b := s.do("GET", "http://"+wl.IP+path, nil)
		return code, string(b)
	}
	all := func() string {
		code, b := call("/all")
		var m map[string]any
		if code != 200 || json.Unmarshal([]byte(b), &m) != nil {
			t.Fatalf("/all: %d %s", code, b)
		}
		return fmt.Sprint(m)
	}
	if code, b := call("/put?k=a&v=1"); code != 200 {
		t.Fatalf("put: %d %s", code, b)
	}
	if m := all(); m != "map[a:1]" {
		t.Fatalf("all: %s", m)
	}

	// Another writer (another replica; here an admin append) puts c. This
	// copy hasn't read since, so it doesn't have c.
	journal := "/v1/tenants/" + e.TenantID + "/workloads/storeprobe/stores/probe/main/journal"
	var page mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(context.Background(), "GET", journal, nil, &page); err != nil {
		t.Fatal(err)
	}
	var ar mgmtapi.StoreAppendResult
	if err := e.Mgmt.Do(context.Background(), "POST", journal, map[string]any{
		"after": page.Head, "incarnation": page.Incarnation,
		"entries": []any{map[string]any{"ckey": "c", "data": []byte(`{"k":"c","v":"from-elsewhere"}`)}},
	}, &ar); err != nil {
		t.Fatalf("append: %v", err)
	}
	appends := e.FM.StoreAppends()
	// Delete c where the local copy says it isn't there: the delete must
	// see the journal's c and delete it.
	if code, b := call("/delif?k=c"); code != 200 {
		t.Fatalf("delete of c: %d %s", code, b)
	}
	if n := e.FM.StoreAppends() - appends; n != 1 {
		t.Errorf("the delete of c appended %d entries to the journal, want 1 (it was acknowledged from the stale copy)", n)
	}
	if m := all(); m != "map[a:1]" {
		t.Fatalf("after the acknowledged delete of c: %s, want map[a:1]", m)
	}

	// mgmt is unreachable: a delete that would change nothing fails like
	// every write, rather than being acknowledged unchecked.
	e.FM.SetStoresDown(true)
	if code, b := call("/delif?k=zz"); code != 500 || !strings.Contains(b, "store_unavailable") {
		t.Fatalf("no-op delete with the journal down: %d %s, want it refused (store_unavailable)", code, b)
	}
	e.FM.SetStoresDown(false)
	if code, b := call("/delif?k=zz"); code != 200 {
		t.Fatalf("no-op delete with the journal back: %d %s", code, b)
	}
}
