package fakemgmt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// Platform stores (ADR-044), in memory: the journal a store
// object appends to and catches up from, fenced by the workload's lease
// like secret grants, with ckey compaction. Not sealed (nothing here is
// persisted).

type fakeStore struct {
	kind, name, inc string
	head            int64
	entries         []fakeStoreEntry // by seq
	created, update time.Time

	// degraded: as its host reported it (storedegraded.go).
	degraded *mgmtapi.StoreDegraded
}

type fakeStoreEntry struct {
	seq  int64
	ckey string
	data []byte
}

func storeKey(r *http.Request) string {
	return mgmtapi.LeaseName(r.PathValue("id"), r.PathValue("workload")) + "|" + r.PathValue("kind") + "|" + r.PathValue("name")
}

// StoreAppends counts journal appends (tests).
func (s *Server) StoreAppends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeAppends
}

// fenceStore: a caller naming an epoch must hold the lease at it.
func (s *Server) fenceStore(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query().Get("epoch")
	if q == "" {
		return true // an admin
	}
	epoch, _ := strconv.ParseInt(q, 10, 64)
	name := mgmtapi.LeaseName(r.PathValue("id"), r.PathValue("workload"))
	l := s.leases[name]
	if l == nil || l.epoch != epoch {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, fmt.Sprintf("lease %s is not held at epoch %d", name, epoch))
		return false
	}
	if hid := r.Header.Get("Tw-Host-ID"); hid != "" && hid != l.host {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, "host "+hid+" does not hold the lease")
		return false
	}
	return true
}

func (s *Server) storeView(r *http.Request, st *fakeStore) map[string]any {
	var bytes int64
	for _, e := range st.entries {
		bytes += int64(len(e.data))
	}
	v := map[string]any{"tenantID": r.PathValue("id"), "workload": r.PathValue("workload"), "kind": st.kind, "name": st.name,
		"incarnation": st.inc, "head": st.head, "entries": len(st.entries), "bytes": bytes, "createdAt": ms(st.created), "updatedAt": ms(st.update)}
	if st.degraded != nil {
		v["degraded"] = st.degraded
	}
	return v
}

func newIncarnation() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "si_" + hex.EncodeToString(b)
}

// createStoreLocked creates the store at r's path if absent.
func (s *Server) createStoreLocked(r *http.Request) (*fakeStore, bool) {
	if s.stores == nil {
		s.stores = map[string]*fakeStore{}
	}
	k := storeKey(r)
	if st := s.stores[k]; st != nil {
		return st, false
	}
	now := time.Now()
	st := &fakeStore{kind: r.PathValue("kind"), name: r.PathValue("name"), inc: newIncarnation(), created: now, update: now}
	s.stores[k] = st
	return st, true
}

func (s *Server) listStores(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	prefix := mgmtapi.LeaseName(r.PathValue("id"), r.PathValue("workload")) + "|"
	kind := r.URL.Query().Get("kind")
	items := []map[string]any{}
	for k, st := range s.stores {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix && (kind == "" || st.kind == kind) {
			items = append(items, s.storeView(r, st))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i]["kind"].(string)+"/"+items[i]["name"].(string), items[j]["kind"].(string)+"/"+items[j]["name"].(string)
		return a < b
	})
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) getStore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stores[storeKey(r)]
	if st == nil {
		writeErr(w, 404, "unknown_store", "no such store")
		return
	}
	writeJSON(w, 200, s.storeView(r, st))
}

func (s *Server) putStore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	st, created := s.createStoreLocked(r)
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, s.storeView(r, st))
}

func (s *Server) deleteStore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey(r)
	if s.stores[k] == nil {
		writeErr(w, 404, "unknown_store", "no such store")
		return
	}
	delete(s.stores, k)
	w.WriteHeader(204)
}

func (s *Server) readJournal(w http.ResponseWriter, r *http.Request) {
	if !s.holdStoreRead(w, r) {
		return
	}
	if s.journalDown(w) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > mgmt.StoreReadMaxEntries {
		limit = mgmt.StoreReadMaxEntries
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fenceStore(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p := mgmtapi.StoreJournalPage{Entries: []mgmtapi.StoreEntry{}}
	st := s.stores[storeKey(r)]
	if st == nil {
		writeJSON(w, 200, p)
		return
	}
	p.Incarnation, p.Head, p.Degraded = st.inc, st.head, st.degraded
	for _, e := range st.entries {
		if e.seq <= after {
			continue
		}
		if len(p.Entries) == limit {
			p.More = true
			break
		}
		p.Entries = append(p.Entries, mgmtapi.StoreEntry{Seq: e.seq, Data: e.data})
	}
	writeJSON(w, 200, p)
}

func (s *Server) appendJournal(w http.ResponseWriter, r *http.Request) {
	if s.journalDown(w) {
		return
	}
	var body struct {
		After       *int64                 `json:"after"`
		Incarnation string                 `json:"incarnation"`
		Entries     []mgmtapi.StoreEntryIn `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil || body.After == nil || len(body.Entries) == 0 {
		writeErr(w, 400, "bad_request", "bad append")
		return
	}
	total := 0
	for _, e := range body.Entries {
		if len(e.Data) > mgmt.StoreEntryMaxBytes {
			writeErr(w, 400, "store_limit", "entry too large")
			return
		}
		total += len(e.Data)
	}
	if total > mgmt.StoreAppendMaxBytes || len(body.Entries) > mgmt.StoreAppendMaxEntries {
		writeErr(w, 400, "store_limit", "append too large")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) || !s.fenceStore(w, r) {
		return
	}
	st := s.stores[storeKey(r)]
	if st == nil {
		if *body.After != 0 || body.Incarnation != "" {
			writeErr(w, 409, mgmtapi.CodeStoreConflict, "the store does not exist: start again from 0")
			return
		}
		st, _ = s.createStoreLocked(r)
	}
	inc := body.Incarnation
	if inc == "" && *body.After == 0 {
		inc = st.inc
	}
	if inc != st.inc || *body.After != st.head {
		writeErr(w, 409, mgmtapi.CodeStoreConflict, fmt.Sprintf("head is %d, not %d", st.head, *body.After))
		return
	}
	for _, e := range body.Entries {
		st.head++
		if e.CKey != "" {
			kept := st.entries[:0]
			for _, old := range st.entries {
				if old.ckey != e.CKey {
					kept = append(kept, old)
				}
			}
			st.entries = kept
		}
		st.entries = append(st.entries, fakeStoreEntry{seq: st.head, ckey: e.CKey, data: e.Data})
	}
	st.update = time.Now()
	s.storeAppends++
	if s.loseAppendAnswerLocked(w) {
		return
	}
	writeJSON(w, 201, mgmtapi.StoreAppendResult{Incarnation: st.inc, Head: st.head})
}
