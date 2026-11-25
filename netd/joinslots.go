package netd

import (
	"context"
	"sync"
)

// joinSlots limits concurrent joins (Config.JoinParallel) and hands slots
// out in the order objects were created. A buffered channel used as a
// semaphore wakes waiters in no particular order, so under a low limit an
// object could keep being passed over, and Rejoin's objects didn't join in
// the order it created them (issue #75). Create takes its place in line
// synchronously (enqueue); the join goroutine then waits for it (wait).
type joinSlots struct {
	mu    sync.Mutex
	free  int
	queue []*slotTicket
}

// slotTicket is one place in line; ch is closed when it holds a slot.
type slotTicket struct{ ch chan struct{} }

func newJoinSlots(n int) *joinSlots { return &joinSlots{free: n} }

// enqueue takes a place in line without blocking.
func (s *joinSlots) enqueue() *slotTicket {
	t := &slotTicket{ch: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free > 0 && len(s.queue) == 0 {
		s.free--
		close(t.ch)
	} else {
		s.queue = append(s.queue, t)
	}
	return t
}

// wait blocks until t holds a slot, or returns ctx.Err() (holding none) if
// ctx ends first.
func (s *joinSlots) wait(ctx context.Context, t *slotTicket) error {
	select {
	case <-t.ch:
		return nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	for i, q := range s.queue {
		if q == t {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.mu.Unlock()
			return ctx.Err()
		}
	}
	s.mu.Unlock()
	// Granted as ctx ended: give the slot back.
	s.release()
	return ctx.Err()
}

// release returns a slot: to the first in line, else to the pool.
func (s *joinSlots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) > 0 {
		t := s.queue[0]
		s.queue = s.queue[1:]
		close(t.ch)
		return
	}
	s.free++
}
