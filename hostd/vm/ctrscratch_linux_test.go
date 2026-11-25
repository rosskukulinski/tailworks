//go:build linux

package vm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// fileContains reports whether the file at p holds needle (streamed: the
// scratch disk is a sparse file of hundreds of MiB).
func fileContains(t *testing.T, p string, needle []byte) bool {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()
	buf := make([]byte, 4<<20)
	var tail []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append(tail, buf[:n]...)
			if bytes.Contains(chunk, needle) {
				return true
			}
			if len(chunk) > len(needle) {
				tail = append([]byte(nil), chunk[len(chunk)-len(needle):]...)
			} else {
				tail = chunk
			}
		}
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
	}
}

// TestContainerScratchFresh (security ledger C-scratch, C-coldboot; the
// Cloudflare Containers cross-tenant disk residue of 2026-09): every
// container VM gets a scratch disk made for it alone, in its own jail root,
// removed with the jail; there is no shared block pool, so a later
// container on the same host never sees an earlier one's blocks. Needs root,
// KVM, TW_VM_CONTAINER_KERNEL, TW_VM_ROOTFS and TW_CTR_IMAGES (make
// e2e-container), on a backend whose warm pool and snapshots are on, as
// tw-hostd's are: both containers must still cold-boot (checkCtrColdBoot).
func TestContainerScratchFresh(t *testing.T) {
	imgs := os.Getenv("TW_CTR_IMAGES")
	if imgs == "" {
		t.Skip("TW_CTR_IMAGES not set")
	}
	b, emulated := ctrSmokeBackend(t)
	st := &image.Store{Dir: filepath.Join(t.TempDir(), "cache"), Sources: []image.Source{image.DirSource{Dir: imgs}}, Emulated: emulated}
	ctx := context.Background()
	img, err := st.Get(ctx, "tna", "tw.local/debian-trixie-python:1")
	if err != nil {
		t.Fatal(err)
	}
	argv, env, err := sandbox.ContainerProcess(img.Config.Config.Entrypoint, img.Config.Config.Cmd, img.Config.Config.Env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := sandbox.ResolveInstance("basic", nil, "")
	vcpus, cpuMax, mem, scratch := res.VMShape()
	boot := func(id string) sandbox.Container {
		t.Helper()
		c, err := b.BootContainer(ctx, sandbox.ContainerSpec{ID: id, Image: img.Path, ScratchMiB: scratch, VCPUs: vcpus, CPUMax: cpuMax, MemMiB: mem,
			Pids: 64, Argv: argv, Env: env, Cwd: img.Config.Config.WorkingDir, User: img.Config.Config.User})
		if err != nil {
			t.Fatalf("boot %s: %v", id, err)
		}
		checkCtrColdBoot(t, b, c.Info())
		return c
	}
	scratchOf := func(c sandbox.Container) string {
		t.Helper()
		j := c.Info().Jail
		if j == nil || j.Chroot == "" {
			t.Fatalf("no jail in %+v", c.Info())
		}
		p := filepath.Join(j.Chroot, "scratch.ext4")
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("scratch disk: %v", err)
		}
		if fi.Size() != int64(scratch)<<20 {
			t.Fatalf("scratch %s is %d bytes, want %d MiB", p, fi.Size(), scratch)
		}
		return p
	}

	// Container 1 (tenant A) writes a marker; it reaches its own scratch
	// disk's blocks (the overlay upper lives there) and nothing else.
	var mb [16]byte
	rand.Read(mb[:])
	marker := "TWSCRATCH" + hex.EncodeToString(mb[:])
	c1 := boot("ctrscratch-a")
	defer c1.Stop(0)
	scratch1 := scratchOf(c1)
	r := runExec(t, c1, sandbox.ExecSpec{Argv: []string{"sh", "-c", "echo " + marker + " > /workspace/m.txt && sync && cat /workspace/m.txt"}}, "")
	if !strings.Contains(r.out, marker) || r.code != 0 {
		t.Fatalf("write marker: %+v", r)
	}
	if !fileContains(t, scratch1, []byte(marker)) {
		t.Fatalf("the marker is not in %s: the scan below would prove nothing", scratch1)
	}
	t.Logf("container 1: marker on its scratch disk %s (%d MiB)", scratch1, scratch)

	// Stop: the VM, its jail root and the scratch disk are gone.
	c1.Stop(2 * time.Second)
	<-c1.Done()
	jail1 := filepath.Dir(scratch1)
	for deadline := time.Now().Add(15 * time.Second); ; {
		_, err := os.Stat(jail1)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jail root %s still exists after Stop (err %v)", jail1, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Container 2 (another tenant, same host, possibly the same slot and
	// path): a new scratch disk with none of container 1's blocks, which the
	// guest formatted from blank, and nothing of the marker inside.
	c2 := boot("ctrscratch-b")
	defer c2.Stop(0)
	scratch2 := scratchOf(c2)
	if fileContains(t, scratch2, []byte(marker)) {
		t.Fatalf("container 2's scratch disk %s holds container 1's marker: disk residue across containers", scratch2)
	}
	// (The writable dirs only: a grep over / walks /proc and /dev and never
	// returns. The host-side scan above is the evidence; this is the view
	// from inside.)
	r = runExec(t, c2, sandbox.ExecSpec{Argv: []string{"sh", "-c", "ls -A /workspace; grep -rsl " + marker + " /workspace /tmp /var/tmp /root /home | head -3; echo done"}}, "")
	if strings.Contains(r.out, "m.txt") || strings.Contains(r.out, marker) || !strings.Contains(r.out, "done") {
		t.Fatalf("container 2 sees container 1's files: %+v", r)
	}
	t.Logf("container 2: new scratch disk %s (same path as container 1: %v), no residue", scratch2, scratch2 == scratch1)
}
