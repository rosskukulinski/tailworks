package netd_test

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// rawUpstream serves each conn on a unix socket with handle, after reading
// the request headers.
func rawUpstream(t *testing.T, handle func(c net.Conn, req *http.Request)) string {
	t.Helper()
	sock := filepath.Join(newRundir(t), "up.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					handle(c, req)
					if req.Close {
						return
					}
				}
			}()
		}
	}()
	return sock
}

// TestKeepEarlyAnswerWithoutConnectionClose: an upstream that answers
// without reading the body and closes, without announcing the close. The
// caller gets the answer, and the next request doesn't fail on the dead
// conn. (TestUnixUpstreamEarlyAnswer is the one that catches #288 itself;
// this and TestKeepEarlyAnswerNoAnswer guard the edge cases of the fix.)
func TestKeepEarlyAnswerWithoutConnectionClose(t *testing.T) {
	t.Parallel()
	sock := rawUpstream(t, func(c net.Conn, req *http.Request) {
		if req.Method == http.MethodPost {
			io.WriteString(c, "HTTP/1.1 413 Request Entity Too Large\r\nContent-Length: 3\r\n\r\nbig")
			c.Close()
			return
		}
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	})
	front := httptest.NewServer(netd.UnixUpstream(sock))
	t.Cleanup(front.Close)
	body := bytes.Repeat([]byte("x"), 200<<10)
	for i := 0; i < 50; i++ {
		resp, err := http.Post(front.URL, "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge || string(b) != "big" {
			t.Fatalf("POST %d: %d %s", i, resp.StatusCode, b)
		}
		resp, err = http.Get(front.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET after POST %d: %d %s", i, resp.StatusCode, b)
		}
	}
}

// TestKeepEarlyAnswerNoAnswer: an upstream that closes mid-body without
// answering still gives the caller a prompt 503.
func TestKeepEarlyAnswerNoAnswer(t *testing.T) {
	t.Parallel()
	sock := rawUpstream(t, func(c net.Conn, req *http.Request) {
		io.CopyN(io.Discard, req.Body, 1000)
		c.Close()
	})
	front := httptest.NewServer(netd.UnixUpstream(sock))
	t.Cleanup(front.Close)
	body := bytes.Repeat([]byte("x"), 200<<10)
	for i := 0; i < 20; i++ {
		st := time.Now()
		resp, err := http.Post(front.URL, "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "upstream_unavailable") {
			t.Fatalf("POST %d: %d %s", i, resp.StatusCode, b)
		}
		if d := time.Since(st); d > 5*time.Second {
			t.Fatalf("POST %d took %v", i, d)
		}
	}
}
