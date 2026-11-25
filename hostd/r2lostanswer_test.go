package hostd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestR2PutLostAnswer: a put whose index write reaches the journal but
// whose answer is lost (mgmt crashed or the connection dropped after the
// commit) fails, yet the object it wrote is in the index. Its blob must
// still be there: the put may not delete what the index may name, or the
// key reads r2_blob_missing until it is overwritten. Found by reading
// store-r2.js while building the R2 chaos loop (e2e/dataplane, #420).
func TestR2PutLostAnswer(t *testing.T) {
	t.Parallel()
	blobDir := t.TempDir()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.R2 = &r2.FS{Root: blobDir}
	})
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("r2probe"), "r2probe", nil)
	wl := e.WaitActive("r2probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	r2op(t, s, base, map[string]any{"op": "put", "key": "k", "value": "one"})

	e.FM.LoseAppendAnswers(1)
	if msg := r2opErr(t, s, base, map[string]any{"op": "put", "key": "k", "value": "two"}); !strings.Contains(msg, "store_unavailable") {
		t.Fatalf("put with its answer lost: %s, want store_unavailable", msg)
	}
	// The journal has "two" (the append committed), so a read catches up
	// to it and must be able to read its bytes.
	time.Sleep(500 * time.Millisecond) // the failed put's blob delete, if any, has run
	got := r2op(t, s, base, map[string]any{"op": "get", "key": "k"})
	if got["text"] != "two" {
		t.Fatalf("get after the lost answer: %v, want \"two\" (the put landed in the journal)", got)
	}
}
