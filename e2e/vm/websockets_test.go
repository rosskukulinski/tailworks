//go:build e2evm && (linux || darwin)

package vm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestE2EVMWebSockets: WebSockets to a Durable Object on the microVM tier
// (e2e/vm/testdata/vmws), in both object modes, with the hibernation API:
// the object accepts with ctx.acceptWebSocket, two clients exchange
// messages and a broadcast, then both idle past workerd's eviction window
// (~10 s). The sockets stay open, the object wakes (a new boot ID) for the
// next message, and each socket's attachment is intact.
func TestE2EVMWebSockets(t *testing.T) {
	s := startVMSuite(t, nil)
	base := s.deploy(vmApp("vmws"), "vmws", nil)
	for _, ns := range []string{"ROOM", "PLAIN"} {
		t.Run(ns, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			dial := func(tag string) *websocket.Conn {
				u := "ws" + strings.TrimPrefix(base, "http") + "/" + ns + "/r1/ws?tag=" + tag
				c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: s.client.HTTPClient(0)})
				if err != nil {
					t.Fatalf("dial %s: %v %v", u, err, resp)
				}
				t.Cleanup(func() { c.CloseNow() })
				return c
			}
			read := func(c *websocket.Conn, typ string) map[string]any {
				t.Helper()
				for {
					_, b, err := c.Read(ctx)
					if err != nil {
						t.Fatalf("read (want %s): %v", typ, err)
					}
					var m map[string]any
					json.Unmarshal(b, &m)
					if m["type"] == typ {
						return m
					}
				}
			}
			send := func(c *websocket.Conn, msg string) {
				t.Helper()
				if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
					t.Fatalf("write %s: %v", msg, err)
				}
			}

			c1, c2 := dial("one"), dial("two")
			boot1 := read(c1, "hello")["boot"]
			read(c2, "hello")
			send(c1, "x")
			if r := read(c1, "reply"); r["n"] != float64(1) || r["tag"] != "one" || r["conns"] != float64(2) {
				t.Fatalf("c1 reply: %v", r)
			}
			send(c2, "all")
			if r := read(c1, "reply"); r["msg"] != "all" || r["tag"] != "two" {
				t.Fatalf("broadcast to c1: %v", r)
			}
			read(c2, "reply")

			// Idle past workerd's eviction: hibernatable sockets stay open.
			time.Sleep(14 * time.Second)
			send(c1, "after")
			r := read(c1, "reply")
			if r["n"] != float64(2) || r["tag"] != "one" || r["conns"] != float64(2) {
				t.Fatalf("after idle: %v (want the attachment {n:2, tag:one} and 2 sockets)", r)
			}
			if r["boot"] == boot1 {
				t.Fatalf("%s was not evicted while idle with hibernatable sockets (boot %v unchanged)", ns, boot1)
			}
			send(c2, "after")
			if r := read(c2, "reply"); r["n"] != float64(2) || r["tag"] != "two" {
				t.Fatalf("c2 after idle: %v", r)
			}
			t.Logf("%s: boot %v -> %v after 14 s idle; sockets and attachments intact", ns, boot1, r["boot"])
			c1.Close(websocket.StatusNormalClosure, "")
			c2.Close(websocket.StatusNormalClosure, "")
		})
	}
}
