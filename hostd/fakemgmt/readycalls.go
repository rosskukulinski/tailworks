package fakemgmt

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
