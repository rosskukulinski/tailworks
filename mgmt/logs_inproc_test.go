package mgmt

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// handlerTransport streams: the first line is readable while the handler is
// still writing (tw logs -f through an embedded host), and status codes
// pass through.
func TestHandlerTransportStreams(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"unknown_workload"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "line 1\n")
		http.NewResponseController(w).Flush()
		<-release
		io.WriteString(w, "line 2\n")
	})
	c := &http.Client{Transport: handlerTransport{h}}
	resp, err := c.Get("http://hostd/v1/logs?n=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("status %d header %v", resp.StatusCode, resp.Header)
	}
	br := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() { l, _ := br.ReadString('\n'); got <- l }()
	select {
	case l := <-got:
		if l != "line 1\n" {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first line not streamed before the handler finished")
	}
	close(release)
	if rest, _ := io.ReadAll(br); string(rest) != "line 2\n" {
		t.Fatalf("rest %q", rest)
	}
	r404, err := c.Get("http://hostd/other")
	if err != nil || r404.StatusCode != 404 {
		t.Fatalf("404 passthrough: %v %v", r404, err)
	}
	r404.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://hostd/v1/logs", nil)
	if _, err := (&http.Client{Transport: handlerTransport{nil}}).Do(req); err == nil {
		t.Fatal("nil handler: want an error")
	}
}
