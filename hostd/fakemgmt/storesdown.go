package fakemgmt

import "net/http"

// SetStoresDown makes the store journal (read and append) answer 503, as an
// unreachable mgmt would, while the rest of the API keeps working (tests of
// a store object failing static).
func (s *Server) SetStoresDown(down bool) {
	s.mu.Lock()
	s.storesDown = down
	s.mu.Unlock()
}

// journalDown answers 503 when SetStoresDown is on.
func (s *Server) journalDown(w http.ResponseWriter) bool {
	s.mu.Lock()
	down := s.storesDown
	s.mu.Unlock()
	if down {
		writeErr(w, 503, "store_unavailable", "the store journal is unreachable (test)")
	}
	return down
}
