//go:build linux

package helper

import (
	"encoding/binary"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeVMM plays Firecracker's side of the uffd handshake against a real
// uffdServer (listenUffd + serve, the production path): it connects,
// sends the region table with a "userfaultfd" (the read end of a pipe, so
// no privileges are needed), queues one page-fault message at addr, and
// returns the error the session ended with.
func fakeVMM(t *testing.T, table []byte, addr uint64, nonzero []uint64) error {
	t.Helper()
	dir, err := os.MkdirTemp("", "uffd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	memFile := filepath.Join(dir, "mem")
	mem := make([]byte, testMemLen)
	for i := range mem {
		mem[i] = 0xa5
	}
	if err := os.WriteFile(memFile, mem, 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := listenUffd(filepath.Join(dir, "uffd.sock"), os.Getuid(), memFile, nonzero)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[1])
	msg := make([]byte, uffdMsgSize)
	msg[0] = uffdEventPagefault
	binary.NativeEndian.PutUint64(msg[16:24], addr)
	if _, err := unix.Write(p[1], msg); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "uffd.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err := c.WriteMsgUnix(table, unix.UnixRights(p[0]), nil); err != nil {
		t.Fatal(err)
	}
	unix.Close(p[0]) // the server has its own copy now
	select {
	case <-u.done:
	case <-time.After(10 * time.Second):
		t.Fatal("uffd session did not end")
	}
	err, _ = u.err.Load().(error)
	return err
}

// TestRT3_H11_UffdBadTableFailsSession: a crafted region table from the
// VMM fails that VM's uffd session with an error, and the helper process
// keeps serving the next one (security/LOOP.md I8). Before the fix the
// "wrapped offset" table passed the handshake check and the queued fault
// indexed the memory file at 2^64-4096, panicking the whole helper (and
// this test binary).
func TestRT3_H11_UffdBadTableFailsSession(t *testing.T) {
	bad := map[string]uffdRegion{
		"wrapped offset":  {Base: testBase, Size: 2 * 4096, Offset: math.MaxUint64 - 4095, PageSize: 4096},
		"offset wraps":    {Base: testBase, Size: 1 << 63, Offset: 1 << 63, PageSize: 4096},
		"unaligned base":  {Base: testBase + 1, Size: 2 * 4096, Offset: testMemLen - 2*4096, PageSize: 4096},
		"zero page size":  {Base: testBase, Size: 4096, PageSize: 0},
		"past end of mem": {Base: testBase, Size: 4096, Offset: testMemLen, PageSize: 4096},
	}
	nonzero := make([]uint64, testMemLen/4096/64)
	for i := range nonzero {
		nonzero[i] = math.MaxUint64
	}
	for name, r := range bad {
		t.Run(name, func(t *testing.T) {
			err := fakeVMM(t, regionsJSON(t, r), testBase, nonzero)
			if err == nil || !strings.Contains(err.Error(), "uffd: bad region") {
				t.Fatalf("session error %v; want the table rejected", err)
			}
		})
	}
	// The helper is still here and serves a good table: the fault gets as
	// far as UFFDIO_COPY (which a pipe refuses).
	good := uffdRegion{Base: testBase, Size: testMemLen, PageSize: 4096}
	err := fakeVMM(t, regionsJSON(t, good), testBase+4096, nil)
	if err == nil || !strings.Contains(err.Error(), "UFFDIO_COPY") {
		t.Fatalf("good table: session error %v; want it to reach UFFDIO_COPY", err)
	}
}

// TestUffdRecoverSession: a panic in a session goroutine becomes that
// session's error instead of killing the helper.
func TestUffdRecoverSession(t *testing.T) {
	u := &uffdServer{}
	func() {
		defer u.recoverSession()
		var mem []byte
		i := 1
		_ = mem[i] // index out of range, as copyPage did before validation
	}()
	err, _ := u.err.Load().(error)
	if err == nil || !strings.Contains(err.Error(), "uffd: panic") {
		t.Fatalf("err = %v; want the recovered panic", err)
	}
}
