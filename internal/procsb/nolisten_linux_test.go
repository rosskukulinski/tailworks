//go:build linux

package procsb_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/procsb"
)

// TestNoListen (ledger H-lan-bind): under NoListen a sandboxed process
// can't make a listener of its own: not on its allowed bind port on
// 0.0.0.0 (Landlock matches the port, not the address), not on another
// address, and not unbound (listen() picks *:<ephemeral>, which Landlock
// doesn't see). It still serves a listener it inherits as fd 3. Without
// NoListen the 0.0.0.0:<port> and unbound probes succeed, which is the
// exposure the flag closes.
func TestNoListen(t *testing.T) {
	needLandlock(t)
	root := t.TempDir()
	own := filepath.Join(root, "own")
	os.MkdirAll(own, 0o700)
	bport := freePort(t)
	port := strconv.Itoa(int(bport))
	cases := map[string]string{
		"own_port_any": "0.0.0.0:" + port,
		"own_port_lo":  "127.0.0.1:" + port,
		"unbound":      "",
	}
	// Without NoListen, the port-only bind rule lets 0.0.0.0:<port> listen.
	os.MkdirAll(filepath.Join(root, "open"), 0o700)
	open := writeProfile(t, filepath.Join(root, "open"), procsb.Profile{Own: []string{own}, BindTCP: []uint16{bport}})
	if abi, _ := procsb.ABI(); abi >= 4 {
		if got := probe(t, open, procsb.ProbeListenTCP, cases["own_port_any"]); got != "ok" {
			t.Fatalf("without NoListen, listen on 0.0.0.0:%s: %s (want ok: the test proves nothing)", port, got)
		}
		if got := probe(t, open, procsb.ProbeListenTCP, ""); got != "ok" {
			t.Logf("without NoListen, an unbound listen: %s (this kernel's Landlock refuses autobind)", got)
		}
	}
	os.MkdirAll(filepath.Join(root, "closed"), 0o700)
	closed := writeProfile(t, filepath.Join(root, "closed"), procsb.Profile{Own: []string{own}, BindTCP: []uint16{bport}, NoListen: true})
	for name, a := range cases {
		if got := probe(t, closed, procsb.ProbeListenTCP, a); got == "ok" {
			t.Errorf("%s: a listener on %q under NoListen", name, a)
		}
	}
	// The inherited listener (hostd's 127.0.0.1 socket) still serves.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd, err := procsb.ProbeCommand(closed, procsb.ProbeAcceptFD)
	if err != nil {
		t.Fatal(err)
	}
	cmd.ExtraFiles = []*os.File{f}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	c, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
	if err == nil {
		c.Close()
	}
	if err := cmd.Wait(); err != nil || strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("accept on the inherited fd under NoListen: %v %q", err, out.String())
	}
}
