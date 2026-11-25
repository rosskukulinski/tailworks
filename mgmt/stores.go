package mgmt

// Platform stores (ADR-044): the durable half of every
// platform-owned storage binding (Workers KV first; D1, Queues and R2 are
// expected to reuse it). A store is served by a platform-owned SQLite
// Durable Object in the workload's workerd (hostd/gen/js/store.js); that
// object appends each change here *before* applying it locally, and
// catches up from here when it starts (a hostd restart, a move to another
// host) or when another writer got in first. So:
//
//   - an acknowledged write is never only on one host's disk;
//   - the journal is linearizable: an append is conditional on the head
//     the writer has applied (store_conflict otherwise);
//   - a host writes and reads only while it holds the workload's lease at
//     the epoch it names (lease_lost), exactly like secret and volume key
//     releases; a fenced-off host can't write;
//   - entries are sealed at rest with a per-store data key in custody, so
//     the database (and its backups) holds no plaintext, and deleting a
//     store crypto-shreds whatever a backup kept.
//
// mgmt never interprets an entry. A kind names a compaction key (ckey) per
// entry when a later entry fully replaces an earlier one (KV: the key), and
// mgmt deletes the superseded entries in the append's transaction, so the
// journal stays the size of the live data.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Store limits.
const (
	StoreEntryMaxBytes    = 2 << 20
	StoreAppendMaxBytes   = 4 << 20
	StoreAppendMaxEntries = 128
	StoreMaxBytes         = 1 << 30
	StoreCKeyMaxBytes     = 1024
	StoreReadMaxEntries   = 1000
	StoreReadMaxBytes     = 4 << 20
	// storeAppendBodyLimit bounds a host's append body (base64 of
	// StoreAppendMaxBytes plus JSON), above the host role's 1 MiB.
	storeAppendBodyLimit = 6 << 20
)

// storeAppendPattern is the append route (the host role's body limit is
// raised for it).
const storeAppendPattern = "POST /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/journal"

var (
	storeKindRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	storeNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ValidStoreKind and ValidStoreName check a store's kind and name.
func ValidStoreKind(s string) bool { return storeKindRE.MatchString(s) }
func ValidStoreName(s string) bool { return storeNameRE.MatchString(s) }

// StoreInfo is a store's metadata (GET …/stores/{kind}/{name}).
type StoreInfo struct {
	TenantID    string `json:"tenantID"`
	Workload    string `json:"workload"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Incarnation string `json:"incarnation"`
	Head        int64  `json:"head"`
	Entries     int64  `json:"entries"`
	Bytes       int64  `json:"bytes"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`

	// Degraded: the journal entry the store's object could not apply, as
	// its host last reported it (storedegraded.go); nil when none does.
	Degraded *StoreDegraded `json:"degraded,omitempty"`
}

// StoreEntryIn is one entry to append: an optional compaction key and the
// kind-encoded data (plaintext; mgmt seals it).
type StoreEntryIn struct {
	CKey string `json:"ckey,omitempty"`
	Data []byte `json:"data"`
}

// StoreEntry is one journal entry as read back (plaintext).
type StoreEntry struct {
	Seq  int64  `json:"seq"`
	Data []byte `json:"data"`
}

// StoreJournalPage is GET …/journal.
type StoreJournalPage struct {
	Incarnation string       `json:"incarnation,omitempty"`
	Head        int64        `json:"head"`
	Entries     []StoreEntry `json:"entries"`
	More        bool         `json:"more"`

	// Degraded is the store's degraded record, so a store object that
	// replays past it can clear it (storedegraded.go).
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

func (m *Mgmt) storeJournal() (storeJournal, error) {
	sj, ok := m.store.(storeJournal)
	if !ok {
		return nil, errf(501, "internal", "this mgmt store has no platform stores")
	}
	return sj, nil
}

// storeScope checks a store's address and resolves the workload whose
// stores these are: a replica ("<workload>--r2") uses its group's, as for
// secrets.
func (m *Mgmt) storeScope(ctx context.Context, tenantID, workload, kind, name string) (string, error) {
	if !validName(workload) {
		return "", errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if !ValidStoreKind(kind) || !ValidStoreName(name) {
		return "", errf(400, "bad_request", "bad store kind %q or name %q", kind, name)
	}
	if tenantID == "" || strings.ContainsAny(tenantID, "/\x00") {
		return "", errf(400, "bad_request", "bad tenant ID")
	}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return "", lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if g, err := m.replicaGroupOf(ctx, tenantID, workload); err != nil {
		return "", err
	} else if g != "" {
		return g, nil
	}
	return workload, nil
}

// fenceStore admits a host to a workload's stores only while it holds the
// workload's lease at epoch (hostID "" on the local-dev socket skips the
// holder check). Admins (no host, no epoch) are not fenced. A host must
// name an epoch.
func (m *Mgmt) fenceStore(ctx context.Context, tenantID, workload, hostID string, epoch *int64, what string) error {
	if epoch == nil {
		if hostID != "" {
			return errf(400, "bad_request", "a host must give the epoch of the lease it holds")
		}
		return nil
	}
	l, err := m.store.GetLease(ctx, LeaseName(tenantID, workload))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errf(409, "lease_lost", "no lease for %s/%s", tenantID, workload)
		}
		return err
	}
	if l.Epoch != *epoch || (hostID != "" && l.Holder != hostID) || l.ExpiresAt < m.now().UnixMilli() {
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "deny", Code: "lease_lost"},
			map[string]any{"host": hostID, "epoch": *epoch, "store": what})
		return errf(409, "lease_lost", "the lease on %s/%s is not held by %q at epoch %d", tenantID, workload, hostID, *epoch)
	}
	return nil
}

// ---- data keys ----

// storeKeys caches each incarnation's data key (custody is a file or a
// network read).
var storeKeys sync.Map // incarnation -> []byte

func storeKeyCustodyName(tenantID, inc string) string {
	return "tw/" + tenantID + "/stores/" + inc + "/key"
}

func (m *Mgmt) storeKey(ctx context.Context, tenantID, inc string) ([]byte, error) {
	if k, ok := storeKeys.Load(inc); ok {
		return k.([]byte), nil
	}
	k, err := m.custody.Get(ctx, storeKeyCustodyName(tenantID, inc))
	if err != nil {
		return nil, fmt.Errorf("custody: store key: %w", err)
	}
	if len(k) != 32 {
		return nil, errors.New("custody: store key: want 32 bytes")
	}
	storeKeys.Store(inc, k)
	return k, nil
}

func subKey(key []byte, label string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("tw-store-v1\n" + label))
	return h.Sum(nil)
}

// storeCKey is what the database keeps of a compaction key: an HMAC, so
// KV keys are not in the database in plaintext.
func storeCKey(key []byte, ckey string) string {
	if ckey == "" {
		return ""
	}
	h := hmac.New(sha256.New, subKey(key, "ckey"))
	h.Write([]byte(ckey))
	return hex.EncodeToString(h.Sum(nil))
}

func storeAAD(inc string, seq int64) []byte {
	return []byte("tw-store-v1\n" + inc + "\n" + strconv.FormatInt(seq, 10))
}

func storeAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(subKey(key, "seal"))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// sealStoreEntry seals one entry, bound to its incarnation and seq.
func sealStoreEntry(key []byte, inc string, seq int64, data []byte) ([]byte, error) {
	g, err := storeAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, data, storeAAD(inc, seq)), nil
}

func openStoreEntry(key []byte, inc string, seq int64, sealed []byte) ([]byte, error) {
	g, err := storeAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("store: truncated entry")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], storeAAD(inc, seq))
	if err != nil {
		return nil, errors.New("store: entry failed authentication")
	}
	return out, nil
}

// ---- operations ----

// CreateStore creates an empty store (created=false: it existed). by is
// who asked (audit).
func (m *Mgmt) CreateStore(ctx context.Context, tenantID, workload, kind, name, by string) (StoreInfo, bool, error) {
	sj, err := m.storeJournal()
	if err != nil {
		return StoreInfo{}, false, err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return StoreInfo{}, false, err
	}
	return m.createStore(ctx, sj, tenantID, scope, kind, name, by)
}

func (m *Mgmt) createStore(ctx context.Context, sj storeJournal, tenantID, scope, kind, name, by string) (StoreInfo, bool, error) {
	if st, err := sj.GetStore(ctx, tenantID, scope, kind, name); err == nil {
		return st, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return StoreInfo{}, false, err
	}
	if err := m.checkStoreCount(ctx, tenantID, "store "+kind+"/"+name); err != nil { // quota.go
		return StoreInfo{}, false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return StoreInfo{}, false, err
	}
	now := m.now().UnixMilli()
	st := StoreInfo{TenantID: tenantID, Workload: scope, Kind: kind, Name: name, Incarnation: newID("si_"), CreatedAt: now, UpdatedAt: now}
	// The key goes to custody first: a store row never exists without it.
	if err := m.custody.Put(ctx, storeKeyCustodyName(tenantID, st.Incarnation), key); err != nil {
		return StoreInfo{}, false, fmt.Errorf("custody: %w", err)
	}
	created, err := sj.CreateStore(ctx, st)
	if err != nil || !created {
		m.custody.Delete(ctx, storeKeyCustodyName(tenantID, st.Incarnation))
		if err != nil {
			return StoreInfo{}, false, err
		}
		st, err = sj.GetStore(ctx, tenantID, scope, kind, name) // a concurrent creator won
		return st, false, err
	}
	storeKeys.Store(st.Incarnation, key)
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "store_create", ActingFor: by},
		map[string]any{"workload": scope, "kind": kind, "name": name, "incarnation": st.Incarnation, "by": by})
	return st, true, nil
}

// GetStore returns one store's metadata.
func (m *Mgmt) GetStore(ctx context.Context, tenantID, workload, kind, name string) (StoreInfo, error) {
	sj, err := m.storeJournal()
	if err != nil {
		return StoreInfo{}, err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return StoreInfo{}, err
	}
	st, err := sj.GetStore(ctx, tenantID, scope, kind, name)
	if err != nil {
		return StoreInfo{}, lookupErr(err, errf(404, "unknown_store", "workload %s has no %s store %q", workload, kind, name))
	}
	return st, nil
}

// ListStores lists a workload's stores (kind "" = every kind).
func (m *Mgmt) ListStores(ctx context.Context, tenantID, workload, kind string, pg Page) ([]StoreInfo, error) {
	sj, err := m.storeJournal()
	if err != nil {
		return nil, err
	}
	if kind != "" && !ValidStoreKind(kind) {
		return nil, errf(400, "bad_request", "bad store kind %q", kind)
	}
	scope, err := m.storeScope(ctx, tenantID, workload, "x", "x")
	if err != nil {
		return nil, err
	}
	return sj.ListStores(ctx, tenantID, scope, kind, pg)
}

// DeleteStore deletes a store, its journal and its data key.
func (m *Mgmt) DeleteStore(ctx context.Context, tenantID, workload, kind, name, by string) error {
	sj, err := m.storeJournal()
	if err != nil {
		return err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return err
	}
	st, err := sj.DeleteStore(ctx, tenantID, scope, kind, name)
	if err != nil {
		return lookupErr(err, errf(404, "unknown_store", "workload %s has no %s store %q", workload, kind, name))
	}
	storeKeys.Delete(st.Incarnation)
	if err := m.custody.Delete(ctx, storeKeyCustodyName(tenantID, st.Incarnation)); err != nil {
		m.log.Warn("store deleted, but its data key is still in custody", "incarnation", st.Incarnation, "err", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "store_delete", ActingFor: by},
		map[string]any{"workload": scope, "kind": kind, "name": name, "incarnation": st.Incarnation, "by": by})
	return nil
}

// AppendStore appends entries to a store's journal, conditional on its
// head being after (and its incarnation inc, when given). A host must hold
// the workload's lease at epoch. The first append (after 0, no
// incarnation) creates the store.
func (m *Mgmt) AppendStore(ctx context.Context, tenantID, workload, kind, name, hostID string, epoch *int64, after int64, inc string, entries []StoreEntryIn) (StoreAppendResult, error) {
	sj, err := m.storeJournal()
	if err != nil {
		return StoreAppendResult{}, err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return StoreAppendResult{}, err
	}
	what := kind + "/" + name
	if err := m.fenceStore(ctx, tenantID, workload, hostID, epoch, what); err != nil {
		return StoreAppendResult{}, err
	}
	if len(entries) == 0 || len(entries) > StoreAppendMaxEntries {
		return StoreAppendResult{}, errf(400, "bad_request", "an append has 1 to %d entries", StoreAppendMaxEntries)
	}
	if after < 0 {
		return StoreAppendResult{}, errf(400, "bad_request", "after must be ≥ 0")
	}
	total := 0
	for _, e := range entries {
		if len(e.Data) > StoreEntryMaxBytes {
			return StoreAppendResult{}, errf(400, "store_limit", "a store entry is at most %d bytes (got %d)", StoreEntryMaxBytes, len(e.Data))
		}
		if len(e.CKey) > StoreCKeyMaxBytes {
			return StoreAppendResult{}, errf(400, "store_limit", "a compaction key is at most %d bytes", StoreCKeyMaxBytes)
		}
		total += len(e.Data)
	}
	if total > StoreAppendMaxBytes {
		return StoreAppendResult{}, errf(400, "store_limit", "an append is at most %d bytes (got %d)", StoreAppendMaxBytes, total)
	}
	st, err := sj.GetStore(ctx, tenantID, scope, kind, name)
	switch {
	case errors.Is(err, ErrNotFound):
		if after != 0 || inc != "" {
			return StoreAppendResult{}, errf(409, "store_conflict", "store %s does not exist (it was deleted): start again from 0", what)
		}
		by := hostID
		if by == "" {
			by = principalOf(ctx).Kind
		}
		if st, _, err = m.createStore(ctx, sj, tenantID, scope, kind, name, by); err != nil {
			return StoreAppendResult{}, err
		}
	case err != nil:
		return StoreAppendResult{}, err
	}
	if inc == "" && after == 0 {
		inc = st.Incarnation // a writer that has applied nothing yet
	}
	key, err := m.storeKey(ctx, tenantID, st.Incarnation)
	if err != nil {
		return StoreAppendResult{}, err
	}
	in := make([]storeEntryIn, len(entries))
	for i, e := range entries {
		in[i] = storeEntryIn{CKey: storeCKey(key, e.CKey), Data: e.Data}
	}
	seal := func(seq int64, data []byte) ([]byte, error) { return sealStoreEntry(key, st.Incarnation, seq, data) }
	quota, err := m.tenantQuotas(ctx, tenantID) // quota.go
	if err != nil {
		return StoreAppendResult{}, err
	}
	st, err = sj.AppendStore(ctx, tenantID, scope, kind, name, inc, after, in, seal, StoreMaxBytes, quota.MaxStoreBytes, m.now().UnixMilli())
	if qe, ok := asQuotaError(err, tenantID, "store "+what); ok {
		return StoreAppendResult{}, qe
	}
	switch {
	case errors.Is(err, errStoreConflict), errors.Is(err, ErrNotFound):
		return StoreAppendResult{}, errf(409, "store_conflict", "store %s moved past %d: catch up and retry", what, after)
	case errors.Is(err, errStoreFull):
		return StoreAppendResult{}, errf(400, "store_limit", "store %s would exceed %d bytes", what, StoreMaxBytes)
	case err != nil:
		return StoreAppendResult{}, err
	}
	return StoreAppendResult{Incarnation: st.Incarnation, Head: st.Head}, nil
}

// ReadStore reads a store's journal after a sequence number. A host must
// hold the workload's lease at epoch.
func (m *Mgmt) ReadStore(ctx context.Context, tenantID, workload, kind, name, hostID string, epoch *int64, after int64, limit int) (StoreJournalPage, error) {
	sj, err := m.storeJournal()
	if err != nil {
		return StoreJournalPage{}, err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return StoreJournalPage{}, err
	}
	if err := m.fenceStore(ctx, tenantID, workload, hostID, epoch, kind+"/"+name); err != nil {
		return StoreJournalPage{}, err
	}
	if limit <= 0 || limit > StoreReadMaxEntries {
		limit = StoreReadMaxEntries
	}
	st, err := sj.GetStore(ctx, tenantID, scope, kind, name)
	if errors.Is(err, ErrNotFound) {
		return StoreJournalPage{Entries: []StoreEntry{}}, nil
	}
	if err != nil {
		return StoreJournalPage{}, err
	}
	key, err := m.storeKey(ctx, tenantID, st.Incarnation)
	if err != nil {
		return StoreJournalPage{}, err
	}
	rows, more, err := sj.ReadStore(ctx, st.Incarnation, after, limit, StoreReadMaxBytes)
	if err != nil {
		return StoreJournalPage{}, err
	}
	p := StoreJournalPage{Incarnation: st.Incarnation, Head: st.Head, Entries: make([]StoreEntry, 0, len(rows)), More: more, Degraded: st.Degraded}
	for _, r := range rows {
		d, err := openStoreEntry(key, st.Incarnation, r.Seq, r.Data)
		if err != nil {
			return StoreJournalPage{}, fmt.Errorf("store %s/%s seq %d: %w", kind, name, r.Seq, err)
		}
		p.Entries = append(p.Entries, StoreEntry{Seq: r.Seq, Data: d})
		if r.Seq > p.Head {
			p.Head = r.Seq // an append committed between the two reads
		}
	}
	return p, nil
}

// ---- /v1 handlers ----

func (a *v1api) ListWorkloadStores(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.ListWorkloadStoresParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rows, err := a.s.M.ListStores(r.Context(), id, workload, str(params.Kind), pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, mkPage(rows, n, func(s StoreInfo) PageKey { return PageKey{ID: s.Kind + "/" + s.Name} }))
}

func (a *v1api) GetWorkloadStore(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName) {
	st, err := a.s.M.GetStore(r.Context(), id, workload, kind, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}

func (a *v1api) PutWorkloadStore(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName) {
	st, created, err := a.s.M.CreateStore(r.Context(), id, workload, kind, name, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, st)
}

func (a *v1api) DeleteWorkloadStore(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName) {
	if err := a.s.M.DeleteStore(r.Context(), id, workload, kind, name, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) ReadStoreJournal(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName, params v1.ReadStoreJournalParams) {
	w.Header().Set("Cache-Control", "no-store")
	var after int64
	if params.After != nil {
		after = *params.After
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	p, err := a.s.M.ReadStore(r.Context(), id, workload, kind, name, hostOf(r), params.Epoch, after, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a *v1api) AppendStoreJournal(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName, params v1.AppendStoreJournalParams) {
	var body struct {
		After       *int64         `json:"after"`
		Incarnation string         `json:"incarnation"`
		Entries     []StoreEntryIn `json:"entries"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	if body.After == nil {
		writeErr(w, r, errf(400, "bad_request", "after is required"))
		return
	}
	res, err := a.s.M.AppendStore(r.Context(), id, workload, kind, name, hostOf(r), params.Epoch, *body.After, body.Incarnation, body.Entries)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, res)
}
