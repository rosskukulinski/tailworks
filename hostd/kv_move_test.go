package hostd_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestWorkersKVMove: a KV namespace survives a hostd restart and a lease
// move to another host (ADR-037; ADR-028 forced takeover), and a
// host that gets the workload back catches up on what was written while it
// didn't hold it; with mgmt's journal down, reads fail static and writes
// fail. Two tw-hostd processes (each with its own data dir and
// workerd) share one mgmt and one tailnet.
func TestWorkersKVMove(t *testing.T) {
	t.Parallel()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("kv"), "kv", nil)
	wl := e.WaitActive("kv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	c := &kvCaller{t: t, s: s, ip: wl.IP}
	const W, F = "/op", "/do/op"
	exp := time.Now().Unix() + 3600

	c.must(W, "KV", "put", "a", "1", map[string]any{"metadata": map[string]any{"v": 1}, "expiration": exp})
	c.must(F, "KV", "put", "b", "2")
	c.must(W, "OTHER", "put", "o", "other")
	check := func(when string, want map[string]any) {
		t.Helper()
		for k, v := range want {
			eq(t, when+": "+k, c.must(W, "KV", "get", k), v)
		}
		l := c.must(W, "KV", "listAll", map[string]any{}).(map[string]any)
		if fmt.Sprint(l["names"]) != fmt.Sprint(sortedKeys(want)) {
			t.Fatalf("%s: keys %v, want %v", when, l["names"], sortedKeys(want))
		}
		eq(t, when+": metadata", c.must(F, "KV", "getWithMetadata", "a"), map[string]any{"value": "1", "metadata": map[string]any{"v": 1}, "cacheStatus": nil})
		eq(t, when+": OTHER", c.must(W, "OTHER", "get", "o"), "other")
	}
	check("before", map[string]any{"a": "1", "b": "2"})

	// hostd restarts (its workerd too).
	e.Restart()
	wl = e.WaitActive("kv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	c.ip = wl.IP
	check("after a hostd restart", map[string]any{"a": "1", "b": "2"})

	// mgmt's journal is unreachable and hostd restarts: the namespace
	// object has synced on this host before, so reads serve its local copy
	// (fail static); writes fail, and nothing is half-written.
	e.FM.SetStoresDown(true)
	e.Restart()
	wl = e.WaitActive("kv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	c.ip = wl.IP
	check("journal down", map[string]any{"a": "1", "b": "2"})
	c.fails("503", W, "KV", "put", "c", "3")
	c.fails("503", F, "KV", "delete", "a")
	e.FM.SetStoresDown(false)
	eq(t, "the refused write did not land", c.must(W, "KV", "get", "c"), nil)
	c.must(W, "KV", "put", "c", "3")

	// The lease moves to another host: a fresh workerd with no DO storage
	// for this workload rebuilds the namespace from the journal.
	h2 := e.StartHost("h2")
	dep2, err := e.FM.MoveWorkload(e.TenantID, "kv", "h2")
	if err != nil {
		t.Fatal(err)
	}
	e2 := *e
	e2.Host = h2
	wl2 := e2.WaitActive("kv", dep2, 60*time.Second)
	s.waitPeer(wl2.IP)
	c.ip = wl2.IP
	check("on h2", map[string]any{"a": "1", "b": "2", "c": "3"})
	c.must(W, "KV", "put", "d", "4")
	c.must(F, "KV", "delete", "b")

	// h1 lost the lease: it stops serving the workload.
	deadline := time.Now().Add(20 * time.Second)
	for workloadStatus(t, e, "kv").PID != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("h1 still runs the workload after the move: %+v", workloadStatus(t, e, "kv"))
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Back to h1, whose local copy stops at "c": it catches up first.
	dep3, err := e.FM.MoveWorkload(e.TenantID, "kv", "h1")
	if err != nil {
		t.Fatal(err)
	}
	wl = e.WaitActive("kv", dep3, 60*time.Second)
	s.waitPeer(wl.IP)
	c.ip = wl.IP
	check("back on h1", map[string]any{"a": "1", "c": "3", "d": "4"})
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
