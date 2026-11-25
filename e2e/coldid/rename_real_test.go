//go:build e2ereal

package coldid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// TestRenameOnAssignmentReal measures, on tailnet 1 (production control),
// what a pre-joined node pool would do on assignment: give an already
// registered node an object's name and tags. Each operation is timed from
// the call until a peer's netmap shows it, and the node must keep
// answering throughout.
//   - hostname: the node changes its own hostname (LocalAPI EditPrefs);
//   - api-name: an admin rename (POST /device/{id}/name);
//   - retag: POST /device/{id}/tags, adding then removing tag:ttest-noadmin
//     (the node keeps tag:ttest, so visibility and grants never change).
//
// Nodes are ttest-fi-<run>-*, ephemeral; both are deleted by ID afterwards.
func TestRenameOnAssignmentReal(t *testing.T) {
	keyFile, tailnet := os.Getenv("TW_E2E_API_KEY_FILE"), os.Getenv("TW_E2E_TAILNET")
	if keyFile == "" || tailnet == "" {
		t.Skip("TW_E2E_API_KEY_FILE / TW_E2E_TAILNET not set")
	}
	if !allowedTailnets[tailnet] {
		t.Fatalf("refusing to run against tailnet %q (not in the allow list)", tailnet)
	}
	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: apiKey}
	b := make([]byte, 3)
	rand.Read(b)
	prefix := "ttest-fi-" + hex.EncodeToString(b)
	tracker := tsapi.NewTracker(api)
	minter := tsapi.MintSource(api, slog.New(netd.NewRedactingHandler(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rep := tracker.Cleanup(cctx, prefix)
		minter.RevokeAll(cctx)
		t.Logf("cleanup: tracked %d, deleted %d, left %d, untracked %v, errors %v", rep.Tracked, rep.Deleted, rep.Left, rep.Untracked, rep.Errors)
		if err := rep.Err(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()
	node := func(name string) *nettest.Node {
		k, err := minter.AuthKey(ctx, netd.KeyRequest{ObjectID: name, Hostname: name, Tags: []string{"tag:ttest"}, Ephemeral: true})
		if err != nil {
			t.Fatal(err)
		}
		n, err := nettest.NewNode(ctx, prodControl, k, name, filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		st, _ := n.LC.StatusWithoutPeers(ctx)
		tracker.Track(string(st.Self.ID), name)
		t.Cleanup(func() { n.Close() })
		return n
	}
	caller, pooled := node(prefix+"-caller"), node(prefix+"-pool")
	if err := pooled.ServeEcho(); err != nil {
		t.Fatal(err)
	}
	pst, _ := pooled.LC.StatusWithoutPeers(ctx)
	stableID := string(pst.Self.ID)
	if err := caller.WaitPeers(ctx, pooled.IP4); err != nil {
		t.Fatal(err)
	}
	httpc := caller.HTTPClient(5 * time.Second)
	reachable := func() bool {
		r, err := httpc.Get("http://" + pooled.IP4 + "/")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}
	// peerSees polls the caller's status until cond holds for the pooled peer.
	peerSees := func(cond func(*ipnstate.PeerStatus) bool) (time.Duration, bool) {
		t0 := time.Now()
		for time.Since(t0) < 60*time.Second {
			if st, err := caller.LC.Status(ctx); err == nil {
				for _, p := range st.Peer {
					if string(p.ID) == stableID && cond(p) {
						return time.Since(t0), true
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		return time.Since(t0), false
	}
	label := func(p *ipnstate.PeerStatus) string { return strings.SplitN(p.DNSName, ".", 2)[0] }
	type sample struct {
		Op        string  `json:"op"`
		CallMS    float64 `json:"callMs"`
		PeerMS    float64 `json:"peerMs"` // call start → the peer's netmap shows it
		Seen      bool    `json:"seen"`
		Reachable bool    `json:"reachable"` // the node still answers afterwards
		Err       string  `json:"err,omitempty"`
	}
	var out []sample
	record := func(op string, call func() error, cond func(*ipnstate.PeerStatus) bool) {
		t0 := time.Now()
		err := call()
		s := sample{Op: op, CallMS: msSince(t0, time.Now())}
		if err != nil {
			s.Err = err.Error()
		} else {
			d, ok := peerSees(cond) // timed from the call's return
			s.PeerMS, s.Seen = s.CallMS+float64(d.Microseconds())/1000, ok
		}
		s.Reachable = reachable()
		out = append(out, s)
		t.Logf("%-9s call %7.1f ms  peer sees it %7.1f ms  seen=%v reachable=%v %s", op, s.CallMS, s.PeerMS, s.Seen, s.Reachable, s.Err)
	}
	const n = 5
	for i := range n {
		name := fmt.Sprintf("%s-h%d", prefix, i)
		record("hostname", func() error {
			_, err := pooled.LC.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{Hostname: name}, HostnameSet: true})
			return err
		}, func(p *ipnstate.PeerStatus) bool { return label(p) == name })
	}
	for i := range n {
		name := fmt.Sprintf("%s-a%d", prefix, i)
		record("api-name", func() error { return api.SetDeviceName(ctx, stableID, name) },
			func(p *ipnstate.PeerStatus) bool { return label(p) == name })
	}
	hasNoadmin := func(p *ipnstate.PeerStatus) bool {
		return p.Tags != nil && slices.Contains(p.Tags.AsSlice(), "tag:ttest-noadmin")
	}
	for range n {
		record("retag+", func() error { return api.SetDeviceTags(ctx, stableID, []string{"tag:ttest", "tag:ttest-noadmin"}) }, hasNoadmin)
		record("retag-", func() error { return api.SetDeviceTags(ctx, stableID, []string{"tag:ttest"}) },
			func(p *ipnstate.PeerStatus) bool { return !hasNoadmin(p) })
	}
	var b2 strings.Builder
	fmt.Fprintf(&b2, "rename on assignment, tailnet 1, n=%d per op\n| op | call p50 | p95 | peer sees it p50 | p95 | max | seen | still reachable |\n|---|---|---|---|---|---|---|---|\n", n)
	for _, op := range []string{"hostname", "api-name", "retag+", "retag-"} {
		var call, peer []float64
		seen, reach, tot := 0, 0, 0
		for _, s := range out {
			if s.Op != op {
				continue
			}
			tot++
			call = append(call, s.CallMS)
			if s.Seen {
				seen++
				peer = append(peer, s.PeerMS)
			}
			if s.Reachable {
				reach++
			}
		}
		fmt.Fprintf(&b2, "| %s | %.0f | %.0f | %.0f | %.0f | %.0f | %d/%d | %d/%d |\n", op, pctl(call, .5), pctl(call, .95),
			pctl(peer, .5), pctl(peer, .95), pctl(peer, 1), seen, tot, reach, tot)
	}
	root, _ := nettest.FindRepoRoot()
	f := filepath.Join(root, "e2e", "coldid", "results", "rename-"+time.Now().Format("20060102-150405")+"-"+prefix+".json")
	os.MkdirAll(filepath.Dir(f), 0o755)
	j, _ := json.MarshalIndent(map[string]any{"samples": out, "table": b2.String()}, "", "  ")
	os.WriteFile(f, j, 0o644)
	t.Logf("results: %s\n%s", f, b2.String())
}
