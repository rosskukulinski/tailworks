package mgmt

import (
	"context"
	"testing"
	"time"
)

// LockName excludes a second holder of the same key until the first
// unlocks, and doesn't block other keys (#578). On Postgres each lock holds
// its own connection, so two callers in one process exclude each other
// as two replicas do. SQLite always; Postgres with TW_TEST_POSTGRES_DSN.
func TestStoreLockName(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		unlock, err := s.LockName(ctx, "secrets/t1/w")
		if err != nil {
			t.Fatal(err)
		}
		got := make(chan time.Time, 1)
		go func() {
			u2, err := s.LockName(ctx, "secrets/t1/w")
			if err != nil {
				t.Error(err)
				got <- time.Time{}
				return
			}
			got <- time.Now()
			u2()
		}()
		other, err := s.LockName(ctx, "secrets/t1/other") // another key: not blocked
		if err != nil {
			t.Fatal(err)
		}
		other()
		select {
		case <-got:
			t.Fatal("a second holder took a held name")
		case <-time.After(300 * time.Millisecond):
		}
		released := time.Now()
		unlock()
		select {
		case at := <-got:
			if at.Before(released) {
				t.Fatalf("second holder got the name at %v, before the release at %v", at, released)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the second holder never got the name after the release")
		}
	})
}
