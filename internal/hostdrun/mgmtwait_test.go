package hostdrun

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestWaitMgmtRetriesAStuckProbe (#245): after a reboot the first request
// to mgmt's node hung in netstack's SYN retransmits (its SYN went out
// before control sent the peer) and hostd reached mgmt ~31 s after boot.
// A probe that hangs is abandoned after mgmtAttempt and a new one answers.
func TestWaitMgmtRetriesAStuckProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	calls := 0
	probe := func(ctx context.Context) error {
		calls++
		if calls == 1 {
			<-ctx.Done() // black-holed: answers only when given up on
			return ctx.Err()
		}
		return nil
	}
	st := time.Now()
	if err := waitMgmt(ctx, probe, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if took, budget := time.Since(st), mgmtAttempt+2*time.Second; took > budget {
		t.Fatalf("mgmt answered after %v (budget %v): the stuck first probe was waited out", took, budget)
	}
	if calls != 2 {
		t.Fatalf("%d probes, want 2", calls)
	}
}

// TestWaitMgmtGivesUp: an unreachable mgmt fails when ctx ends.
func TestWaitMgmtGivesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	err := waitMgmt(ctx, func(context.Context) error { return errors.New("no route") }, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("waitMgmt returned nil for an unreachable mgmt")
	}
}
