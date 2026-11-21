package hostd_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestPlatformStore exercises the platform-store substrate
// (ADR-044) end to end with the test-only "probe" kind on real
// workerd: the platform's Store object behind a binding in the worker and
// in a facet, journal-first writes through hostd to (fake) mgmt, catch-up
// after another writer (an admin) appended, an admin delete starting a new
// incarnation, and recovery of the whole store from the journal after the
// host fenced the workload and lost its DO storage (a move to a new host,
// as far as the store can tell).
func TestPlatformStore(t *testing.T) {
	hostdtest.RegisterProbeStore()
	const fenceAfter = time.Second
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.FenceAfter, o.HeartbeatEvery = fenceAfter, 100*time.Millisecond
	})
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("storeprobe"), "storeprobe", nil)
	wl := e.WaitActive("storeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	call := func(path string, kv ...string) map[string]any {
		t.Helper()
		q := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		code, m := s.getJSON("http://" + wl.IP + path + "?" + q.Encode())
		if code != 200 {
			t.Fatalf("%s %v: %d %v", path, kv, code, m)
		}
		return m
	}

	// Writes from the worker and from a facet reach the same store.
	if r := call("/put", "k", "a", "v", "1"); r["seq"] != float64(1) {
		t.Fatalf("put a: %v", r)
	}
	if r := call("/do/put", "k", "b", "v", "2"); r["seq"] != float64(2) {
		t.Fatalf("facet put b: %v", r)
	}
	call("/put", "k", "a", "v", "1b") // supersedes seq 1 in the journal
	if r := call("/do/get", "k", "a"); r["v"] != "1b" {
		t.Fatalf("facet get a: %v", r)
	}
	if n := e.FM.StoreAppends(); n != 3 {
		t.Fatalf("journal appends %d, want 3 (one per write, before it applies)", n)
	}
	journal := "/v1/tenants/" + e.TenantID + "/workloads/storeprobe/stores/probe/main/journal"
	var page mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(context.Background(), "GET", journal, nil, &page); err != nil {
		t.Fatal(err)
	}
	if page.Head != 3 || len(page.Entries) != 2 { // a@1 compacted away
		t.Fatalf("journal: %v", page)
	}

	// Another writer (an admin, tw kv key put) appends: the store object
	// catches up on its next read that wants fresh data, and its next
	// write conflicts once, catches up and retries.
	var ar mgmtapi.StoreAppendResult
	if err := e.Mgmt.Do(context.Background(), "POST", journal, map[string]any{
		"after": 3, "incarnation": page.Incarnation,
		"entries": []any{map[string]any{"ckey": "c", "data": []byte(`{"k":"c","v":"from-admin"}`)}},
	}, &ar); err != nil || ar.Head != 4 {
		t.Fatalf("admin append: %v %+v", err, ar)
	}
	if r := call("/put", "k", "d", "v", "4"); r["seq"] != float64(5) {
		t.Fatalf("put after an admin append: %v (want seq 5: caught up past 4)", r)
	}
	want := map[string]any{"a": "1b", "b": "2", "c": "from-admin", "d": "4"}
	if r := call("/all"); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("all: %v, want %v", r, want)
	}
	call("/del", "k", "b")
	delete(want, "b")

	// The host fences the workload (no heartbeat), and its DO storage is
	// lost; when it gets the workload back, the store is rebuilt from the
	// journal alone.
	ws := workloadStatus(t, e, "storeprobe")
	if ws.DOStorage == "" {
		t.Fatal("no DO storage dir in status")
	}
	e.FM.SetHeartbeatsDown(true)
	deadline := time.Now().Add(10 * time.Second)
	for workloadStatus(t, e, "storeprobe").PID != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("workload not fenced: %+v", workloadStatus(t, e, "storeprobe"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.RemoveAll(ws.DOStorage); err != nil {
		t.Fatal(err)
	}
	e.FM.SetHeartbeatsDown(false)
	wl = e.WaitActive("storeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	if r := call("/get", "k", "c"); r["v"] != "from-admin" {
		t.Fatalf("after losing DO storage, get c: %v", r)
	}
	if r := call("/all"); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("after losing DO storage, all: %v, want %v", r, want)
	}

	// An entry the kind can't apply (here: not JSON) stops the replay
	// there (#106): the store is degraded, never synced past it. Reads
	// serve what applied before it (not the good entry after it), writes
	// are refused with store_degraded.
	degradeStore(t, e, journal, []byte(`not json`), []byte(`{"k":"after","v":"x"}`))
	if r := call("/all"); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("degraded store, all: %v, want %v (nothing from the failing entry on)", r, want)
	}
	if code, _, body := s.do("GET", "http://"+wl.IP+"/put?k=y&v=1", nil); code != 500 || !strings.Contains(string(body), "[store_degraded]") || !strings.Contains(string(body), "failed to apply") {
		t.Fatalf("write to a degraded store: %d %s", code, body)
	}

	// An admin deletes the store: the object empties itself on its next
	// sync, and the next write starts a new incarnation from seq 1 (which
	// also clears the degraded state).
	if err := e.Mgmt.Do(context.Background(), "DELETE", "/v1/tenants/"+e.TenantID+"/workloads/storeprobe/stores/probe/main", nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := call("/all"); len(r) != 0 {
		t.Fatalf("after delete: %v", r)
	}
	if r := call("/put", "k", "z", "v", "new"); r["seq"] != float64(1) {
		t.Fatalf("first write of the new incarnation: %v", r)
	}

	// The journal never saw anything outside the bound store, and nothing
	// was logged with a value.
	if st := workloadStatus(t, e, "storeprobe"); strings.Contains(st.Error, "from-admin") {
		t.Fatalf("status error carries data: %s", st.Error)
	}
}

func workloadStatus(t *testing.T, e *hostdtest.Env, workload string) hostd.WorkloadStatus {
	t.Helper()
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == workload {
			return w
		}
	}
	return hostd.WorkloadStatus{}
}
