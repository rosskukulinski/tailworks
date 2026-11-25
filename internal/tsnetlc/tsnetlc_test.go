package tsnetlc_test

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsnetlc"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// wait is how long stray waits for each step before it gives up.
const wait = 30 * time.Second

// stray leaves the kind of LocalAPI connection that held Close on real
// hosts: a connection the transport dialed for a request that no longer
// needs it (here: one given up on during its dial) is pooled with no
// request on it, so the server sees it as new, "active" for 5 s.
//
// Each step waits on a signal, not a timer (#304). It used to give the
// request a 50 ms deadline, slow the dial by 200 ms and sleep 400 ms. Under
// load the transport's dial goroutine could start after the deadline, and
// net/http then skips a dial whose request is gone, so there was no stray
// connection and plain ts.Close returned at once. Now the dial is held
// until the request has given up, and stray returns once the dial has
// succeeded. A successful dial is a server-side fact: memnet's Dial returns
// only once the LocalAPI server's Accept has taken the connection, and
// http.Server tracks an accepted connection before Shutdown can return. If
// the transport pools the connection only after tsnetlc.Close's
// CloseIdleConnections, it closes it instead (that call makes the transport
// close newly idle connections).
func stray(t *testing.T, ts *tsnet.Server, lc *local.Client) {
	t.Helper()
	tsnetlc.CloseIdle(ts) // the next request dials
	started, release, dialed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	tsnetlc.SetDialHook(func(dial func() (net.Conn, error)) (net.Conn, error) {
		held := false
		once.Do(func() { held = true })
		if !held {
			return dial()
		}
		close(started)
		<-release
		c, err := dial()
		dialed <- err
		return c, err
	})
	defer tsnetlc.SetDialHook(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reqErr := make(chan error, 1)
	go func() {
		_, err := lc.StatusWithoutPeers(ctx)
		reqErr <- err
	}()
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("the request never dialed")
	}
	cancel()
	select {
	case err := <-reqErr:
		if err == nil {
			t.Fatal("the request outlived its context")
		}
	case <-time.After(wait):
		t.Fatal("the request did not give up on its context")
	}
	close(release)
	select {
	case err := <-dialed:
		if err != nil {
			t.Fatalf("the given-up request's dial: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("the given-up request's dial never completed")
	}
}

// TestCloseDrainsLocalAPIConns (#245): tsnetlc.Close closes a node whose
// LocalAPI client left an unused connection in its pool without waiting
// out the LocalAPI server's 5 s shutdown grace. Plain ts.Close on the same
// state does wait: what tw-hostd's node did on about half of the
// `systemctl --user restart tw` runs on a real host.
func TestCloseDrainsLocalAPIConns(t *testing.T) {
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	for _, c := range []struct {
		name     string
		close    func(*tsnet.Server) error
		min, max time.Duration
	}{
		{"ts.Close (holds)", (*tsnet.Server).Close, 4 * time.Second, time.Minute},
		{"tsnetlc.Close", tsnetlc.Close, 0, 2500 * time.Millisecond},
	} {
		ts := &tsnet.Server{Hostname: "tsnetlc", Dir: filepath.Join(t.TempDir(), "n"), ControlURL: ctl.URL, AuthKey: nettest.DummyKey,
			Ephemeral: true, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
		lc, err := tsnetlc.Client(ts)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = ts.Up(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		stray(t, ts, lc)
		st := time.Now()
		c.close(ts)
		took := time.Since(st)
		tsnetlc.Forget(ts)
		t.Logf("%s: %v", c.name, took.Round(time.Millisecond))
		if took < c.min || took > c.max {
			t.Errorf("%s took %v, want %v–%v", c.name, took.Round(time.Millisecond), c.min, c.max)
		}
	}
}
