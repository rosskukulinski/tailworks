package netd

import (
	"context"
	"testing"
	"time"
)

// TestJoinSlotsOrder: with one slot, tickets are granted in enqueue order;
// a waiter whose context ends leaves the line without taking a slot.
func TestJoinSlotsOrder(t *testing.T) {
	s := newJoinSlots(1)
	ctx := context.Background()
	first := s.enqueue()
	if err := s.wait(ctx, first); err != nil {
		t.Fatal(err)
	}
	tickets := []*slotTicket{s.enqueue(), s.enqueue(), s.enqueue()}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.wait(cctx, tickets[1]); err == nil {
		t.Fatal("cancelled wait got a slot")
	}
	got := make(chan int, 3)
	for _, i := range []int{2, 0} { // start the later waiter first
		go func(i int) {
			if s.wait(ctx, tickets[i]) == nil {
				got <- i
			}
		}(i)
	}
	time.Sleep(10 * time.Millisecond)
	s.release() // first's slot → tickets[0], not tickets[2]
	if i := <-got; i != 0 {
		t.Fatalf("slot went to ticket %d, want 0", i)
	}
	s.release()
	if i := <-got; i != 2 {
		t.Fatalf("slot went to ticket %d, want 2", i)
	}
	s.release()
	if s.free != 1 || len(s.queue) != 0 {
		t.Fatalf("free %d, queue %d after all released", s.free, len(s.queue))
	}
}
