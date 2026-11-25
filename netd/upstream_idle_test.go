package netd_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestWorkerdKeepsIdleConnLongerThanUpstream: workerd must not close an idle
// ingress connection before UnixUpstream drops it itself, or net/http can
// reuse a connection workerd is closing and fail the request (#287). This
// fails if a workerd upgrade shortens its idle close (15 s today) to within
// 2 s of netd.UpstreamIdleTimeout.
func TestWorkerdKeepsIdleConnLongerThanUpstream(t *testing.T) {
	t.Parallel()
	wdBin, err := nettest.WorkerdBinary()
	if err != nil {
		t.Fatal(err)
	}
	rundir := newRundir(t)
	wd, err := nettest.StartWorkerd(wdBin, "testdata/workerd/config.capnp", rundir, filepath.Join(rundir, "egress.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wd.Stop)

	c, err := net.Dial("unix", wd.IngressSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	get := func() {
		t.Helper()
		if _, err := fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: workerd\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.Close {
			t.Fatal("workerd answered Connection: close")
		}
	}
	get()
	idle := netd.UpstreamIdleTimeout + 2*time.Second
	c.SetReadDeadline(time.Now().Add(idle))
	if _, err := br.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("workerd closed an idle ingress connection within %v (UnixUpstream keeps them %v): %v", idle, netd.UpstreamIdleTimeout, err)
	}
	get() // and it still serves on it
}
