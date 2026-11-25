package fakemgmt

import "net/http"

// storeReadHold makes hosts' journal reads hang, as a slow or black-holed
// mgmt would (tests of the store read deadline, #38).
type storeReadHold struct {
	release   chan struct{} // non-nil while reads are held; closed to let them go
	reads     int           // host journal reads received
	held      int           // reads waiting now
	abandoned int           // held reads whose caller gave up
}

// HoldStoreReads makes the hosts' store journal reads (those naming a lease
// epoch; admin reads are unaffected) wait until HoldStoreReads(false) or
// until the caller gives up. Appends are unaffected.
func (s *Server) HoldStoreReads(hold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.storeReadHold
	switch {
	case hold && h.release == nil:
		h.release = make(chan struct{})
	case !hold && h.release != nil:
		close(h.release)
		h.release = nil
	}
}

// StoreReadStats reports the hosts' journal reads so far: received, waiting
// in HoldStoreReads now, and abandoned by their caller while held.
func (s *Server) StoreReadStats() (reads, held, abandoned int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.storeReadHold
	return h.reads, h.held, h.abandoned
}

// holdStoreRead counts a host's journal read and holds it while reads are
// held. It reports false (having answered 503) if the caller went away
// meanwhile.
func (s *Server) holdStoreRead(w http.ResponseWriter, r *http.Request) bool {
	if !r.URL.Query().Has("epoch") {
		return true
	}
	s.mu.Lock()
	h := &s.storeReadHold
	h.reads++
	rel := h.release
	if rel == nil {
		s.mu.Unlock()
		return true
	}
	h.held++
	s.mu.Unlock()
	ok := true
	select {
	case <-rel:
	case <-r.Context().Done():
		ok = false
	}
	s.mu.Lock()
	h.held--
	if !ok {
		h.abandoned++
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, 503, "store_unavailable", "the caller gave up on a held journal read (test)")
	}
	return ok
}
