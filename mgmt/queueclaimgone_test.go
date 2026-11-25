package mgmt

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// claimHookStore is a store whose ClaimQueue calls after once it has
// committed: the moment a caller can leave with the claim already made.
type claimHookStore struct {
	*SQLStore
	mu    sync.Mutex
	after func()
}

func (s *claimHookStore) ClaimQueue(ctx context.Context, tenantID, name, inc string, c queueClaim) ([]queueMsgRow, int, error) {
	rows, dead, err := s.SQLStore.ClaimQueue(ctx, tenantID, name, inc, c)
	if len(rows) == 0 {
		return rows, dead, err
	}
	s.mu.Lock()
	f := s.after
	s.after = nil
	s.mu.Unlock()
	if f != nil {
		f()
	}
	return rows, dead, err
}

// failingWriter is a response writer whose connection is gone.
type failingWriter struct{ h http.Header }

func (w *failingWriter) Header() http.Header       { return w.h }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

// TestQueueStoreClaimForGoneCallerReleased (#412): a pull whose caller
// left while the claim committed (hostd stopped the consumer loop), and a
// pull whose response could not be written, hand their messages straight
// back. They are visible again at once, and the attempt the claim counted
// is taken back: the next delivery is attempt 1, not 2 after a visibility
// timeout. On SQLite, and on Postgres under make test-pg.
func TestQueueStoreClaimForGoneCallerReleased(t *testing.T) {
	t.Parallel()
	for name, st := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			hs := &claimHookStore{SQLStore: st}
			cust, err := OpenFileCustody(filepath.Join(dir, "c"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := New(Config{Store: hs, Custody: cust, Broker: &Broker{}, IDSecret: []byte("0123456789abcdef"), ReapInterval: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			tn, err := m.CreateTenant(ctx, TenantRequest{Tailnet: "t.ts.net", MintMode: MintOAuth, OAuthClientFile: writeTestOAuthFile(t, filepath.Join(dir, "api.env"))})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.RegisterHost(ctx, RegisterRequest{HostID: "h1", CapacityNodes: 10, Tiers: []string{"process"}}, "", ""); err != nil {
				t.Fatal(err)
			}
			dep, err := m.Deploy(ctx, DeployRequest{TenantID: tn.ID, Workload: "support", Host: "h1",
				Bundle: makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "wrangler.jsonc": queueWrangler, "dist/index.js": "export default {}"})})
			if err != nil {
				t.Fatal(err)
			}
			host := QueueCaller{HostID: "h1", Workload: "support", Epoch: &dep.Epoch}
			send := func(bodies ...string) {
				t.Helper()
				var in []QueueMessageIn
				for _, b := range bodies {
					in = append(in, QueueMessageIn{Body: []byte(b), ContentType: "text"})
				}
				if _, err := m.SendQueue(ctx, tn.ID, "jobs", host, in, nil); err != nil {
					t.Fatal(err)
				}
			}
			// pullNow claims what is visible now, as a caller that stays.
			pullNow := func() QueuePullResult {
				t.Helper()
				r, err := m.PullQueue(ctx, tn.ID, "jobs", host, QueuePullOpts{})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			wantBack := func(what string, n int) {
				t.Helper()
				r := pullNow()
				if len(r.Messages) != n {
					t.Fatalf("%s: %d message(s) visible again, want %d: %+v", what, len(r.Messages), n, r)
				}
				var acks []string
				for _, msg := range r.Messages {
					if msg.Attempts != 1 {
						t.Fatalf("%s: delivered as attempt %d, want 1 (the lost claim's attempt was counted)", what, msg.Attempts)
					}
					acks = append(acks, msg.LeaseID)
				}
				if res, err := m.AckQueue(ctx, tn.ID, "jobs", host, QueueAckReq{Acks: acks}); err != nil || res.Acked != n {
					t.Fatalf("%s: ack: %+v %v", what, res, err)
				}
			}

			// A long-polling caller leaves just as a send wakes it and its
			// claim commits.
			pctx, cancel := context.WithCancel(ctx)
			defer cancel()
			hs.mu.Lock()
			hs.after = cancel
			hs.mu.Unlock()
			got := make(chan QueuePullResult, 1)
			go func() {
				r, _ := m.PullQueue(pctx, tn.ID, "jobs", host, QueuePullOpts{WaitMs: 10_000})
				got <- r
			}()
			time.Sleep(100 * time.Millisecond)
			send("a", "b")
			select {
			case r := <-got:
				if len(r.Messages) != 0 {
					t.Fatalf("a caller that left got %+v", r)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("the cancelled pull did not return")
			}
			wantBack("cancelled mid-claim", 2)

			// The answer can't be written: the connection is gone.
			send("c")
			r := pullNow()
			if len(r.Messages) != 1 {
				t.Fatalf("pull: %+v", r)
			}
			m.writePulled(&failingWriter{h: http.Header{}}, tn.ID, "jobs", r)
			wantBack("response not written", 1)
		})
	}
}
