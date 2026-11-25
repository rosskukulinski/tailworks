package fakemgmt

// QueueClaims is how many messages hosts' pulls have claimed from a queue
// so far (tests; a message released and claimed again counts twice). Admin
// pulls don't count.
func (s *Server) QueueClaims(tenantID, queue string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queueLocked(tenantID, queue, false); q != nil {
		return q.claims
	}
	return 0
}
