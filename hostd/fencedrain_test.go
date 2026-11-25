package hostd_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestFenceClosesOpenWebSockets (#572, data-plane routing loop): when a
// stateful workload's lease is revoked (here: it moves to another host),
// its old copy stops answering at once, on WebSockets that are already
// open too. hostd used to stop workerd first (SIGTERM, then up to 3 s for
// it to drain) and reset the node's flows only after that, so the fenced
// copy kept answering open sockets for up to 3 s after the revoke.
func TestFenceClosesOpenWebSockets(t *testing.T) {
	t.Parallel()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("wsfence"), "wsfence", nil)
	wl := e.WaitActive("wsfence", dep, 60*time.Second)
	s.waitPeer(wl.IP)

	var ws *websocket.Conn
	for i := 0; ; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, _, err := websocket.Dial(ctx, "ws://"+wl.IP+"/ws", &websocket.DialOptions{HTTPClient: s.client.HTTPClient(0)})
		cancel()
		if err == nil {
			ws = c
			break
		}
		if i > 50 {
			t.Fatalf("dial: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer ws.CloseNow()

	var (
		mu      sync.Mutex
		replies []time.Time
	)
	closed := make(chan time.Time, 1)
	go func() {
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				closed <- time.Now()
				return
			}
			mu.Lock()
			replies = append(replies, time.Now())
			mu.Unlock()
		}
	}()
	// The socket answers before the move.
	if err := ws.Write(context.Background(), websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		n := len(replies)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no echo before the move")
		}
	}

	// Keep talking on the open socket while the lease moves to h2: h1's
	// next heartbeat carries the revoke and it undeploys the workload.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := ws.Write(ctx, websocket.MessageText, []byte(fmt.Sprint(i)))
			cancel()
			if err != nil {
				return
			}
		}
	}()
	e.StartHost("h2")
	if _, err := e.FM.MoveWorkload(e.TenantID, "wsfence", "h2"); err != nil {
		t.Fatal(err)
	}
	// revoked: when h1 starts stopping the workload (its status turns
	// "stopped" before anything else in undeploy).
	var revoked time.Time
	for deadline := time.Now().Add(30 * time.Second); revoked.IsZero(); time.Sleep(2 * time.Millisecond) {
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == "wsfence" && w.Status == "stopped" {
				revoked = time.Now()
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("h1 never stopped the workload after the move")
		}
	}
	var closedAt time.Time
	select {
	case closedAt = <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the open WebSocket to the fenced copy was not closed within 10 s of the revoke")
	}
	mu.Lock()
	defer mu.Unlock()
	const slack = 300 * time.Millisecond // replies already on the wire, and the status poll
	late := 0
	for _, r := range replies {
		if r.After(revoked.Add(slack)) {
			late++
		}
	}
	t.Logf("closed %v after the revoke; %d replies in all, %d more than %v after it", closedAt.Sub(revoked), len(replies), late, slack)
	if late > 0 {
		t.Errorf("the fenced copy answered %d messages on an open WebSocket more than %v after its lease was revoked", late, slack)
	}
	if closedAt.Sub(revoked) > time.Second {
		t.Errorf("the open WebSocket was closed %v after the revoke, want ≤ 1 s", closedAt.Sub(revoked))
	}
}
