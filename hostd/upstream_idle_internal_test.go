package hostd

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNodeKeepsIdleConnLongerThanUpstream: a Node http.Server with default
// settings must not close an idle keep-alive connection before the
// Node-tier transport drops it itself, or net/http can reuse a connection
// Node is closing and fail the request (#309). It measures when the Node on
// PATH closes an idle connection and fails if that is within 1 s of
// nodeUpstreamIdleTimeout.
func TestNodeKeepsIdleConnLongerThanUpstream(t *testing.T) {
	t.Parallel()
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	const js = `const s = require("http").createServer((q, r) => r.end("ok"));
s.listen(0, "127.0.0.1", () => console.log(process.version, s.address().port));`
	cmd := exec.Command(bin, "-e", js)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("node did not start: %v", err)
	}
	var version, port string
	if _, err := fmt.Sscan(strings.TrimSpace(line), &version, &port); err != nil {
		t.Fatalf("node printed %q: %v", line, err)
	}

	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	if _, err := fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: app\r\n\r\n"); err != nil {
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
		t.Fatal("node answered Connection: close")
	}
	idleAt := time.Now()
	c.SetReadDeadline(idleAt.Add(30 * time.Second))
	_, err = br.ReadByte()
	closed := time.Since(idleAt)
	if err != io.EOF {
		t.Fatalf("idle connection: want EOF from node's idle close, got %v after %v", err, closed)
	}
	t.Logf("node %s closed an idle keep-alive connection after %v (Node-tier transport keeps them %v)", version, closed.Round(time.Millisecond), nodeUpstreamIdleTimeout)
	if floor := nodeUpstreamIdleTimeout + time.Second; closed < floor {
		t.Fatalf("node %s closed an idle connection after %v, less than 1 s after nodeUpstreamIdleTimeout (%v)", version, closed, nodeUpstreamIdleTimeout)
	}
}
