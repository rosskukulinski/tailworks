package fakemgmt

import (
	"strings"
	"time"
)

// StoreSnapshot is one platform store's journal, for `tw dev` to keep
// stores (KV namespaces) between runs: it exports a tenant's stores when it
// stops and imports them into the next run's tenant (whose ID differs).
type StoreSnapshot struct {
	Workload    string               `json:"workload"`
	Kind        string               `json:"kind"`
	Name        string               `json:"name"`
	Incarnation string               `json:"incarnation"`
	Head        int64                `json:"head"`
	Entries     []StoreSnapshotEntry `json:"entries"`
}

// StoreSnapshotEntry is one journal entry.
type StoreSnapshotEntry struct {
	Seq  int64  `json:"seq"`
	CKey string `json:"ckey,omitempty"`
	Data []byte `json:"data"`
}

// ExportStores returns every store of tenantID.
func (s *Server) ExportStores(tenantID string) []StoreSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []StoreSnapshot{}
	for k, st := range s.stores {
		wl, ok := strings.CutPrefix(k, tenantID+"/")
		if !ok {
			continue
		}
		wl, _, _ = strings.Cut(wl, "|")
		snap := StoreSnapshot{Workload: wl, Kind: st.kind, Name: st.name, Incarnation: st.inc, Head: st.head, Entries: []StoreSnapshotEntry{}}
		for _, e := range st.entries {
			snap.Entries = append(snap.Entries, StoreSnapshotEntry{Seq: e.seq, CKey: e.ckey, Data: e.data})
		}
		out = append(out, snap)
	}
	return out
}

// ImportStores adds stores to tenantID (replacing any with the same
// workload, kind and name).
func (s *Server) ImportStores(tenantID string, snaps []StoreSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stores == nil {
		s.stores = map[string]*fakeStore{}
	}
	now := time.Now()
	for _, sn := range snaps {
		st := &fakeStore{kind: sn.Kind, name: sn.Name, inc: sn.Incarnation, head: sn.Head, created: now, update: now}
		for _, e := range sn.Entries {
			st.entries = append(st.entries, fakeStoreEntry{seq: e.Seq, ckey: e.CKey, data: e.Data})
		}
		s.stores[tenantID+"/"+sn.Workload+"|"+sn.Kind+"|"+sn.Name] = st
	}
}
