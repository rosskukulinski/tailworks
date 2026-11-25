package mgmtapi

import (
	"context"
	"fmt"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// Platform stores (ADR-044): the journal of a workload's
// store, read and appended by the lease holder at its epoch.

// CodeStoreConflict: an append's after/incarnation is not the head.
const CodeStoreConflict = "store_conflict"

// StoreEntryIn is one entry to append.
type StoreEntryIn struct {
	CKey string `json:"ckey,omitempty"`
	Data []byte `json:"data"`
}

// StoreEntry is one journal entry.
type StoreEntry struct {
	Seq  int64  `json:"seq"`
	Data []byte `json:"data"`
}

// StoreJournalPage is GET …/stores/{kind}/{name}/journal.
type StoreJournalPage struct {
	Incarnation string       `json:"incarnation,omitempty"`
	Head        int64        `json:"head"`
	Entries     []StoreEntry `json:"entries"`
	More        bool         `json:"more"`

	// Degraded is mgmt's degraded record of the store (storestatus.go):
	// a store object that replays past it reports itself healthy.
	Degraded *StoreDegraded `json:"degraded,omitempty"`
}

// String never prints entry data.
func (p StoreJournalPage) String() string {
	return fmt.Sprintf("StoreJournalPage{%s head %d, %d entries [REDACTED], more %v}", p.Incarnation, p.Head, len(p.Entries), p.More)
}

// StoreAppendResult is POST …/journal's answer.
type StoreAppendResult struct {
	Incarnation string `json:"incarnation"`
	Head        int64  `json:"head"`
}

// ReadStoreJournal reads a store's journal after seq after, as the lease
// holder at epoch.
func (c *Client) ReadStoreJournal(ctx context.Context, tenantID, workload, kind, name string, epoch, after int64, limit int) (StoreJournalPage, error) {
	p := &v1.ReadStoreJournalParams{Epoch: &epoch, After: &after}
	if limit > 0 {
		p.Limit = &limit
	}
	resp, err := c.V1().ReadStoreJournalWithResponse(ctx, tenantID, workload, kind, name, p)
	if err != nil {
		return StoreJournalPage{}, err
	}
	var out StoreJournalPage
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}

// AppendStoreJournal appends entries if the journal's head is after (and
// its incarnation inc, when given), as the lease holder at epoch. A
// writer that is behind gets an *Error with Code CodeStoreConflict.
func (c *Client) AppendStoreJournal(ctx context.Context, tenantID, workload, kind, name string, epoch, after int64, inc string, entries []StoreEntryIn) (StoreAppendResult, error) {
	body := v1.StoreAppend{After: after, Entries: make([]v1.StoreEntryIn, len(entries))}
	if inc != "" {
		body.Incarnation = &inc
	}
	for i, e := range entries {
		body.Entries[i].Data = e.Data
		if e.CKey != "" {
			k := e.CKey
			body.Entries[i].Ckey = &k
		}
	}
	resp, err := c.V1().AppendStoreJournalWithResponse(ctx, tenantID, workload, kind, name, &v1.AppendStoreJournalParams{Epoch: &epoch}, body)
	if err != nil {
		return StoreAppendResult{}, err
	}
	var out StoreAppendResult
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}
