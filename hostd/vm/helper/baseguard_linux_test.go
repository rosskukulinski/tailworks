//go:build linux

package helper

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"golang.org/x/sys/unix"
)

// vmmCalls is a Firecracker API client that fails every call and counts
// them: a refusal must come before the helper talks to the VMM.
type vmmCalls int

func (n *vmmCalls) RoundTrip(*http.Request) (*http.Response, error) {
	*n++
	return nil, errors.New("fake VMM: no calls expected")
}

func wantBadRequest(t *testing.T, what string, err error) {
	t.Helper()
	var he *helperapi.Err
	if !errors.As(err, &he) || he.Code != helperapi.CodeBadRequest {
		t.Errorf("%s: got %v, want a %s refusal", what, err, helperapi.CodeBadRequest)
	}
}

// TestRT3_H16_BaseGuard: the helper itself decides which VM may become a
// base snapshot (security ledger H16). Only a session started with Base
// qualifies; a claimed or loaded VM (a volume slot, no DO volume: what the
// old check accepted), a VM with a volume and a restored copy are refused
// and left running; a base session is refused attach. Runs without KVM or
// root: every refusal happens before the helper touches the VMM or a
// volume.
func TestRT3_H16_BaseGuard(t *testing.T) {
	s := &Server{}
	for _, c := range []struct {
		name string
		sess *session
	}{
		// A pool VM claimed by a workload without a DO volume: its bundle
		// and MAC key are in guest RAM, and the old check let it through.
		{"claimed pool VM, no volume", &session{s: s, slot2: true}},
		{"pool VM with its volume attached", &session{s: s, slot2: true, vol: &attached{}}},
		{"restored copy of a base", &session{s: s, slot2: true, base: &base{}}},
		{"cold boot with a volume", &session{s: s, vol: &attached{}}},
		{"cold boot, no volume", &session{s: s}},
		{"started as base, then got a volume", &session{s: s, slot2: true, forBase: true, vol: &attached{}}},
	} {
		var calls vmmCalls
		c.sess.api = &http.Client{Transport: &calls}
		b, _, err := c.sess.snapshotBase(t.TempDir())
		if b != nil {
			t.Errorf("%s: snapshot-base accepted", c.name)
		}
		wantBadRequest(t, c.name+": snapshot-base", err)
		if calls > 0 {
			t.Errorf("%s: snapshot-base reached the VMM (%d API calls)", c.name, calls)
		}
		if c.sess.stopped {
			t.Errorf("%s: a refused snapshot-base stopped the VM", c.name)
		}
	}

	bs := &session{s: s, slot2: true, forBase: true}
	if err := bs.canSnapshotBase(); err != nil {
		t.Errorf("base session: snapshot-base refused: %v", err)
	}
	if err := (&session{s: s, slot2: true}).canAttach(); err != nil {
		t.Errorf("pool session: attach refused: %v", err)
	}
	// The guard runs before the image fd or the key is used.
	_, err := bs.attach(nil, &helperapi.Volume{Name: "t/w", Epoch: 1, Key: make([]byte, 32)})
	wantBadRequest(t, "base session: attach", err)
	if bs.vol != nil {
		t.Error("base session: a volume was attached")
	}

	// Base only on a fresh pool boot; start refuses it before allocating
	// a slot or touching the host.
	for _, c := range []struct {
		name string
		mod  func(*helperapi.StartRequest)
		fds  int
	}{
		{"no volume slot", func(r *helperapi.StartRequest) { r.VolumeSlot = false }, 4},
		{"container image", func(r *helperapi.StartRequest) { r.Image = true }, 5},
		{"restore", func(r *helperapi.StartRequest) { r.Restore = "tw-base1" }, 4},
	} {
		req := &helperapi.StartRequest{Name: "base", MemMiB: 256, VCPUs: 1, MTU: 1500, GuestMAC: "06:00:ac:10:00:02",
			GuestCID: 3, VsockPort: 1024, VolumeSlot: true, Base: true}
		c.mod(req)
		var fds []int
		for range c.fds {
			f, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			fd, err := unix.Dup(int(f.Fd()))
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			fds = append(fds, fd)
		}
		sess, _, _, err := s.start(nil, os.Getuid(), req, fds)
		if sess != nil {
			t.Errorf("start base with %s: accepted", c.name)
		}
		wantBadRequest(t, "start base with "+c.name, err)
	}
}
