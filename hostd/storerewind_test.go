package hostd_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestPlatformStoreJournalRewound: mgmt's store goes back in time (tw
// restore of an older backup, or an upgrade's rollback): the journal's head
// is now behind what the host's store object has applied, with the same
// incarnation. The object starts over from the journal (it is
// authoritative), so reads stop serving the writes the restore undid and
// the next write lands right after the restored head instead of
// conflicting forever (issue #120).
func TestPlatformStoreJournalRewound(t *testing.T) {
	t.Parallel()
	hostdtest.RegisterProbeStore()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
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

	call("/put", "k", "a", "v", "1")
	call("/put", "k", "b", "v", "2")
	backup := e.FM.ExportStores(e.TenantID) // the backup: head 2
	call("/put", "k", "c", "v", "lost")
	if r := call("/put", "k", "a", "v", "1-lost"); r["seq"] != float64(4) {
		t.Fatalf("put before the restore: %v", r)
	}
	e.FM.ImportStores(e.TenantID, backup) // the restore: head 2, same incarnation

	want := map[string]any{"a": "1", "b": "2"}
	if r := call("/all"); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("after the restore, all: %v, want %v (the journal's state)", r, want)
	}
	if r := call("/put", "k", "d", "v", "4"); r["seq"] != float64(3) {
		t.Fatalf("first write after the restore: %v (want seq 3, right after the restored head)", r)
	}
	want["d"] = "4"
	if r := call("/all"); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("after a write, all: %v, want %v", r, want)
	}
}
