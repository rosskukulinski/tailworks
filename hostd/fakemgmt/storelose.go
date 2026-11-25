package fakemgmt

import "net/http"

// LoseAppendAnswers makes the next n store journal appends commit and then
// answer 503 store_unavailable, as when mgmt crashes or the connection
// drops after the commit and before the answer: the caller can't tell the
// write landed.
func (s *Server) LoseAppendAnswers(n int) {
	s.mu.Lock()
	s.loseAppendAnswers = n
	s.mu.Unlock()
}

// loseAppendAnswerLocked answers 503 for an append that has committed,
// while LoseAppendAnswers has some left. s.mu is held.
func (s *Server) loseAppendAnswerLocked(w http.ResponseWriter) bool {
	if s.loseAppendAnswers <= 0 {
		return false
	}
	s.loseAppendAnswers--
	writeErr(w, 503, "store_unavailable", "the append committed but its answer was lost (test)")
	return true
}
