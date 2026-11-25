package netd_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestUnixUpstreamEarlyAnswer (#288): when workerd answers a POST without
// reading its body (a killed object's 410, a handler rejecting an upload)
// and then closes the connection, the client must get that answer, not
// 503 upstream_unavailable "write: broken pipe" from the body write that
// workerd's close cut off. Before the fix (netd.KeepEarlyAnswer), 18-189
// of the 200 20 KB POSTs and 1-51 of the 200 KB ones failed that way
// (macOS, with and without -race).
func TestUnixUpstreamEarlyAnswer(t *testing.T) {
	t.Parallel()
	wdBin, err := nettest.WorkerdBinary()
	if err != nil {
		t.Fatal(err)
	}
	rundir := newRundir(t)
	wd, err := nettest.StartWorkerd(wdBin, "testdata/early-answer/config.capnp", rundir, filepath.Join(rundir, "egress.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wd.Stop)
	front := httptest.NewServer(netd.UnixUpstream(wd.IngressSock))
	t.Cleanup(front.Close)

	const n = 200
	for _, path := range []string{"/", "/read"} {
		for _, size := range []int{200, 20 << 10, 200 << 10} {
			t.Run(fmt.Sprintf("%s/%d", path, size), func(t *testing.T) {
				body := bytes.Repeat([]byte("x"), size)
				bad := 0
				for i := 0; i < n; i++ {
					resp, err := http.Post(front.URL+path, "application/octet-stream", bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					b, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusGone || string(b) != "gone" {
						if bad == 0 {
							t.Errorf("POST %d: %d %s", i, resp.StatusCode, b)
						}
						bad++
					}
				}
				if bad > 0 {
					t.Errorf("%d of %d POSTs did not get workerd's 410", bad, n)
				}
			})
		}
	}
}
