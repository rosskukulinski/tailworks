package fakemgmt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// Degraded platform stores (#147): a store object's reports (degraded at
// an entry, healthy again) and an operator's repair (skip that entry), as
// mgmt/storedegraded.go does them, without the audit records.

func (s *Server) reportStoreStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Incarnation string                 `json:"incarnation"`
		Applied     int64                  `json:"applied"`
		Degraded    *mgmtapi.StoreDegraded `json:"degraded"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.Incarnation == "" {
		writeErr(w, 400, "bad_request", "bad status report")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fenceStore(w, r) {
		return
	}
	st := s.stores[storeKey(r)]
	if st == nil || st.inc != body.Incarnation {
		w.WriteHeader(204) // another incarnation: ignored
		return
	}
	host := r.Header.Get("Tw-Host-ID")
	switch d := body.Degraded; {
	case d != nil:
		if d.Seq < 1 {
			writeErr(w, 400, "bad_request", "degraded.seq must be ≥ 1")
			return
		}
		rec := *d
		rec.Host = host
		st.degraded = &rec
	case st.degraded != nil && (st.degraded.Host == host || body.Applied >= st.degraded.Seq):
		st.degraded = nil
	}
	w.WriteHeader(204)
}

func (s *Server) repairStore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Skip int64 `json:"skip"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.Skip < 1 {
		writeErr(w, 400, "bad_request", "skip must be a journal seq ≥ 1")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stores[storeKey(r)]
	if st == nil {
		writeErr(w, 404, "unknown_store", "no such store")
		return
	}
	if st.degraded == nil || st.degraded.Seq != body.Skip {
		writeErr(w, 409, "store_not_degraded", fmt.Sprintf("%s store %s is not degraded at journal entry %d", st.kind, st.name, body.Skip))
		return
	}
	kept := st.entries[:0]
	for _, e := range st.entries {
		if e.seq != body.Skip {
			kept = append(kept, e)
		}
	}
	st.entries, st.degraded, st.update = kept, nil, time.Now()
	writeJSON(w, 200, s.storeView(r, st))
}
