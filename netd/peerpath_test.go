package netd_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestEgressWaitsForNewPeerPath: object a calls a node that has just
// registered, as soon as it is in a's netmap (before its first netcheck, so
// before it has a home DERP or endpoints). Without awaitPeerPath the first
// handshake initiation has no path, is dropped, and WireGuard retries after
// 5 s; with it the call completes as soon as the path exists.
//
// The new peer can be in a's netmap, and answer a's disco ping, a moment
// before its object opens the ingress listener (Ready). A SYN that arrives
// then is reset: "connection was refused" (issue #139, on Linux CI and on
// a loaded Mac). The reset came back over WireGuard, so the handshake this
// test is about already worked; the call then waits for Ready and dials
// again, and the time counts from the first dial. Waiting for Ready before
// the first dial instead would give a the path first and hide the 5 s
// penalty. The host's own routes don't matter: tsnet dials a tailnet IP
// through netstack once the peer is in the netmap, which the call waits for.
func TestEgressWaitsForNewPeerPath(t *testing.T) {
	ctl := startControl(t)
	mint := &countingMint{}
	regA, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: filepath.Join(newRundir(t), "a"), Keys: mint.source()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { regA.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a, err := regA.Create("a", netd.ObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	lcA, err := a.Server().LocalClient() // netd set OmitAuth (don't write it again: races its watchers)
	if err != nil {
		t.Fatal(err)
	}

	// call creates a fresh node on another registry (not a sibling) and
	// GETs it through a's egress the moment it appears in a's netmap.
	call := func(id string) time.Duration {
		regB, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: filepath.Join(newRundir(t), id), Keys: mint.source(),
			Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })})
		if err != nil {
			t.Fatal(err)
		}
		defer regB.Close()
		b, err := regB.Create(id, netd.ObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var ip string
		for ip == "" {
			st, err := lcA.Status(ctx)
			if err == nil {
				for _, p := range st.Peer {
					if p.HostName == "tw-"+id && len(p.TailscaleIPs) > 0 {
						ip = p.TailscaleIPs[0].String()
					}
				}
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			time.Sleep(5 * time.Millisecond)
		}
		t0 := time.Now()
		c, err := regA.DialEgress(ctx, "a", ip+":80")
		if err != nil && strings.Contains(err.Error(), "refused") {
			t.Logf("%s: refused before its listener was open; waiting for Ready", id)
			if err := b.Ready(ctx); err != nil {
				t.Fatal(err)
			}
			c, err = regA.DialEgress(ctx, "a", ip+":80")
		}
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", ip)
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", id, resp.StatusCode)
		}
		return time.Since(t0)
	}

	old := netd.PeerPathWait
	netd.PeerPathWait = 0
	without := call("b0")
	netd.PeerPathWait = old
	with := call("b1")
	t.Logf("first request to a just-registered peer: without the path wait %v, with it %v", without.Round(time.Millisecond), with.Round(time.Millisecond))
	if with > 2*time.Second {
		t.Errorf("with the path wait the first request took %v; want well under WireGuard's 5 s retry", with)
	}
}

// TestSetHostname: a ready node renames itself in place: its own netmap
// and a peer's show the new name, object.json keeps it for a rejoin, and
// the node ID and IP are unchanged.
func TestSetHostname(t *testing.T) {
	ctl := startControl(t)
	mint := &countingMint{}
	root := filepath.Join(newRundir(t), "s")
	reg, err := netd.New(netd.Config{ControlURL: ctl.URL, StateRoot: root, Keys: mint.source(),
		Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, _ := reg.Create("pool-aaaa", netd.ObjectOptions{Hostname: "tw-pool-aaaa"})
	q, _ := reg.Create("peer", netd.ObjectOptions{})
	if err := p.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	before := p.Info()
	t0 := time.Now()
	dns, err := p.SetHostname(ctx, "support-abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("renamed in %v → %s", time.Since(t0).Round(time.Millisecond), dns)
	after := p.Info()
	if !strings.HasPrefix(dns, "support-abcdef.") || after.Hostname != "support-abcdef" || after.NodeID != before.NodeID || after.IPs[0] != before.IPs[0] {
		t.Fatalf("before %+v\nafter %+v (dns %s)", before, after, dns)
	}
	lc, _ := q.Server().LocalClient() // netd set OmitAuth
	for dl := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, _ := lc.Status(ctx)
		seen := false
		for _, ps := range st.Peer {
			seen = seen || strings.HasPrefix(ps.DNSName, "support-abcdef.")
		}
		if seen {
			break
		}
		if time.Now().After(dl) {
			t.Fatal("the peer never saw the new name")
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, "pool-aaaa", "object.json"))
	if !strings.Contains(string(b), `"hostname":"support-abcdef"`) {
		t.Fatalf("object.json not updated: %s", b)
	}
}
