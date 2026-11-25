package netd

import (
	"context"
	"errors"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
)

// TestStatusWithName (#599, found by the e2e/dataplane containers loop): a
// join must not count the node ready before its own MagicDNS name is in
// its netmap. statusWithName keeps reading status until the name is there,
// returns the last status when none comes within nameWait or ctx (a
// control with no DNS names still joins), and passes ctx's error on when
// it never read one.
func TestStatusWithName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n := 0
	named := func(context.Context) (*ipnstate.Status, error) {
		n++
		st := &ipnstate.Status{Self: &ipnstate.PeerStatus{}}
		if n >= 3 {
			st.Self.DNSName = "h1-box-a.t.ts.net."
		}
		return st, nil
	}
	st, err := statusWithName(ctx, named)
	if err != nil || st.Self.DNSName != "h1-box-a.t.ts.net." || n != 3 {
		t.Fatalf("got %+v, %v after %d reads; want the name on the 3rd", st, err, n)
	}

	// No name ever (bounded by ctx here): the last status, no error.
	nameless := func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{Self: &ipnstate.PeerStatus{}}, nil
	}
	c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	st, err = statusWithName(c, nameless)
	if err != nil || st == nil || st.Self.DNSName != "" || time.Since(t0) > 5*time.Second {
		t.Fatalf("nameless: %+v %v after %v", st, err, time.Since(t0))
	}

	// Status never readable: ctx's error.
	failing := func(context.Context) (*ipnstate.Status, error) { return nil, errors.New("no backend") }
	c2, cancel2 := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel2()
	if _, err := statusWithName(c2, failing); err == nil {
		t.Fatal("failing status: no error")
	}
}
