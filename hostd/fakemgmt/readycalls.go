package fakemgmt

import (
	"net/http"
	"time"
)

// countReadyLocked records a ready PATCH for objectID (s.mu held).
func (s *Server) countReadyLocked(objectID string) {
	if s.readyCalls == nil {
		s.readyCalls = map[string]int{}
	}
	s.readyCalls[objectID]++
}

// ReadyCalls counts the ready PATCHes hostd sent for objectID (tests: one
// node start reports ready once, issue #133).
func (s *Server) ReadyCalls(objectID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readyCalls[objectID]
}

// SetLeaseLostReady makes the next n ready PATCHes fail with lease_lost
// (tests: a node that joined at a lease epoch a `tw deploy` has since
// bumped, issue #210). Refused PATCHes are not counted by ReadyCalls.
func (s *Server) SetLeaseLostReady(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseLostReady = n
}

// refuseReadyLocked consumes one SetLeaseLostReady refusal (s.mu held).
func (s *Server) refuseReadyLocked() bool {
	if s.leaseLostReady <= 0 {
		return false
	}
	s.leaseLostReady--
	return true
}

// ReadyFault is a test fault on the host's ready reports (PATCH
// /v1/objects/{id}): each one is answered only after Delay, and with Fail
// it then fails with 503 store_unavailable (issue #331: a node whose
// certificate is not issued yet).
type ReadyFault struct {
	Delay time.Duration
	Fail  bool
}

// SetReadyFault applies f to every ready report until it is reset with the
// zero ReadyFault.
func (s *Server) SetReadyFault(f ReadyFault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyFault = f
}

// readyFaulted applies the ReadyFault and reports whether it answered.
func (s *Server) readyFaulted(w http.ResponseWriter) bool {
	s.mu.Lock()
	f := s.readyFault
	s.mu.Unlock()
	return f.Apply(w)
}

// Apply waits f.Delay and, with f.Fail, answers 503 and returns true.
func (f ReadyFault) Apply(w http.ResponseWriter) bool {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
	if !f.Fail {
		return false
	}
	writeErr(w, http.StatusServiceUnavailable, "store_unavailable", "ready reports fail (test fault)")
	return true
}
