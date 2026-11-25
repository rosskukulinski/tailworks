package hostd_test

import (
	"context"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// degradeStore appends entries to a platform store's journal as an admin
// (the path a journal written by another tailworks, or a corrupt entry,
// would take): the first is one the store's kind can't apply, the rest
// are fine. A store object that catches up must stop before the first
// and never report itself synced past it (#106). Returns the failing
// entry's seq.
func degradeStore(t *testing.T, e *hostdtest.Env, journal string, entries ...[]byte) int64 {
	t.Helper()
	var p mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(context.Background(), "GET", journal, nil, &p); err != nil {
		t.Fatal(err)
	}
	in := make([]any, len(entries))
	for i, d := range entries {
		in[i] = map[string]any{"data": d}
	}
	var ar mgmtapi.StoreAppendResult
	if err := e.Mgmt.Do(context.Background(), "POST", journal, map[string]any{"after": p.Head, "incarnation": p.Incarnation, "entries": in}, &ar); err != nil {
		t.Fatalf("admin append: %v", err)
	}
	return p.Head + 1
}

// degradedRecord is mgmt's record of a store's degraded state (#147), nil
// when there is none.
func degradedRecord(t *testing.T, e *hostdtest.Env, store string) *mgmtapi.StoreDegraded {
	t.Helper()
	var st struct {
		Degraded *mgmtapi.StoreDegraded `json:"degraded"`
	}
	if err := e.Mgmt.Do(context.Background(), "GET", store, nil, &st); err != nil {
		t.Fatal(err)
	}
	return st.Degraded
}
