package mgmt

// Workers KV, operator view (ADR-037): `tw kv key get|put|delete|
// list` read and write a KV namespace (a platform store of kind "kv",
// ADR-044) through mgmt. The store substrate never interprets
// entries; this view is the one place in mgmt that decodes KV's entries
// (internal/kvwire, the same format the platform's kv.js writes).
//
// Reads fold the namespace's journal (compacted by key, so it is the size
// of the live data). Writes are conditional journal appends, exactly like
// a binding's put(), so running workloads see them on their next sync.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/kvwire"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// KVKey is one key of a list.
type KVKey struct {
	Name       string          `json:"name"`
	Expiration int64           `json:"expiration,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// KVKeyPage is a page of ListKV.
type KVKeyPage struct {
	Keys         []KVKey `json:"keys"`
	ListComplete bool    `json:"listComplete"`
	Cursor       string  `json:"cursor,omitempty"`
}

// KVValue is one value.
type KVValue struct {
	Name       string          `json:"name"`
	Value      []byte          `json:"value"`
	Expiration int64           `json:"expiration,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// String never prints the value.
func (v KVValue) String() string { return "KVValue{" + v.Name + " [REDACTED]}" }

// KVPut is a write.
type KVPut struct {
	Value         []byte          `json:"value"`
	Expiration    int64           `json:"expiration,omitempty"`
	ExpirationTTL int64           `json:"expirationTtl,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

// kvFold reads a namespace's whole journal and folds it to its live keys.
func (m *Mgmt) kvFold(ctx context.Context, tenantID, workload, name string) (map[string]kvwire.Item, StoreInfo, error) {
	st, err := m.GetStore(ctx, tenantID, workload, kvwire.Kind, name)
	if err != nil {
		return nil, StoreInfo{}, err
	}
	var data [][]byte
	var after int64
	for {
		p, err := m.ReadStore(ctx, tenantID, workload, kvwire.Kind, name, "", nil, after, 0)
		if err != nil {
			return nil, StoreInfo{}, err
		}
		if p.Incarnation != st.Incarnation {
			return nil, StoreInfo{}, errf(409, "store_conflict", "KV namespace %s was deleted or recreated during the read; retry", name)
		}
		for _, e := range p.Entries {
			data = append(data, e.Data)
			after = e.Seq
		}
		if p.Head > st.Head {
			st.Head = p.Head
		}
		if !p.More {
			break
		}
	}
	items, err := kvwire.Fold(data, m.now().Unix())
	if err != nil {
		return nil, StoreInfo{}, err
	}
	return items, st, nil
}

func kvKeyOf(it kvwire.Item) KVKey {
	k := KVKey{Name: it.Key, Expiration: it.Expiration}
	if it.Metadata != "" {
		k.Metadata = json.RawMessage(it.Metadata)
	}
	return k
}

// ListKV lists a namespace's live keys (KV's list()).
func (m *Mgmt) ListKV(ctx context.Context, tenantID, workload, name, prefix, cursor string, limit int) (KVKeyPage, error) {
	after := ""
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return KVKeyPage{}, errf(400, "bad_request", "invalid cursor")
		}
		after = string(b)
	}
	items, _, err := m.kvFold(ctx, tenantID, workload, name)
	if err != nil {
		return KVKeyPage{}, err
	}
	page, next := kvwire.Page(items, prefix, after, limit)
	out := KVKeyPage{Keys: make([]KVKey, 0, len(page)), ListComplete: next == ""}
	for _, it := range page {
		out.Keys = append(out.Keys, kvKeyOf(it))
	}
	if next != "" {
		out.Cursor = base64.RawURLEncoding.EncodeToString([]byte(next))
	}
	return out, nil
}

// GetKV returns one live value.
func (m *Mgmt) GetKV(ctx context.Context, tenantID, workload, name, key string) (KVValue, error) {
	if err := kvwire.CheckKey(key); err != nil {
		return KVValue{}, errf(400, "bad_request", "%v", err)
	}
	items, _, err := m.kvFold(ctx, tenantID, workload, name)
	if err != nil {
		return KVValue{}, err
	}
	it, ok := items[key]
	if !ok {
		return KVValue{}, errf(404, "not_found", "KV namespace %s has no such key", name)
	}
	k := kvKeyOf(it)
	return KVValue{Name: key, Value: it.Value, Expiration: k.Expiration, Metadata: k.Metadata}, nil
}

// kvAppend appends one entry for key, built by entry from the namespace's
// current live keys (nil: write nothing), retrying when another writer got
// in first. A write is audited as kind (kv_put, kv_delete) with the
// namespace and the actor, never the key.
func (m *Mgmt) kvAppend(ctx context.Context, tenantID, workload, name, key, kind, by string, entry func(map[string]kvwire.Item) []byte) error {
	for attempt := 0; attempt < 5; attempt++ {
		items, st, err := m.kvFold(ctx, tenantID, workload, name)
		if err != nil {
			return err
		}
		data := entry(items)
		if data == nil {
			return nil
		}
		res, err := m.AppendStore(ctx, tenantID, workload, kvwire.Kind, name, "", nil, st.Head, st.Incarnation, []StoreEntryIn{{CKey: key, Data: data}})
		var e *Error
		if errors.As(err, &e) && e.Code == "store_conflict" {
			continue
		}
		if err == nil {
			m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: kind, ActingFor: by},
				map[string]any{"workload": st.Workload, "kind": kvwire.Kind, "name": name, "incarnation": res.Incarnation, "seq": res.Head, "by": by})
		}
		return err
	}
	return errf(409, "store_conflict", "KV namespace %s kept changing under this write; retry", name)
}

// PutKV writes one value, with Cloudflare's rules for keys, expirations,
// metadata and sizes.
func (m *Mgmt) PutKV(ctx context.Context, tenantID, workload, name, key, by string, p KVPut) (KVKey, error) {
	if err := kvwire.CheckKey(key); err != nil {
		return KVKey{}, errf(400, "bad_request", "%v", err)
	}
	if len(p.Value) > kvwire.MaxValueBytes {
		return KVKey{}, errf(400, "store_limit", "value length of %d exceeds limit of %d", len(p.Value), kvwire.MaxValueBytes)
	}
	now := m.now().Unix()
	x := p.Expiration
	if p.ExpirationTTL != 0 {
		if p.ExpirationTTL < kvwire.MinTTLSeconds {
			return KVKey{}, errf(400, "bad_request", "expirationTtl must be at least %d", kvwire.MinTTLSeconds)
		}
		x = now + p.ExpirationTTL
	} else if x != 0 && x < now+kvwire.MinTTLSeconds {
		return KVKey{}, errf(400, "bad_request", "expiration must be at least %d seconds in the future", kvwire.MinTTLSeconds)
	}
	meta := ""
	if len(p.Metadata) > 0 && string(p.Metadata) != "null" {
		meta = compactJSON(p.Metadata)
		if err := kvwire.CheckMetadata(meta); err != nil {
			return KVKey{}, errf(400, "bad_request", "%v", err)
		}
	}
	h := kvwire.Header{Key: key, Expiration: x, Metadata: meta}
	if err := m.kvAppend(ctx, tenantID, workload, name, key, "kv_put", by, func(map[string]kvwire.Item) []byte { return kvwire.Encode(h, p.Value) }); err != nil {
		return KVKey{}, err
	}
	return kvKeyOf(kvwire.Item{Key: key, Expiration: x, Metadata: meta}), nil
}

// DeleteKV deletes one key (nothing is written if it does not exist).
func (m *Mgmt) DeleteKV(ctx context.Context, tenantID, workload, name, key, by string) error {
	if err := kvwire.CheckKey(key); err != nil {
		return errf(400, "bad_request", "%v", err)
	}
	return m.kvAppend(ctx, tenantID, workload, name, key, "kv_delete", by, func(items map[string]kvwire.Item) []byte {
		if _, ok := items[key]; !ok {
			return nil
		}
		return kvwire.Encode(kvwire.Header{Key: key, Delete: true}, nil)
	})
}

// compactJSON writes metadata the way JSON.stringify does (what a
// binding's put() stores), so the 1024-byte limit means the same.
func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw) // CheckMetadata refuses it
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// ---- /v1 handlers ----

func (a *v1api) ListKVKeys(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName, params v1.ListKVKeysParams) {
	w.Header().Set("Cache-Control", "no-store")
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	p, err := a.s.M.ListKV(r.Context(), id, workload, name, str(params.Prefix), str(params.Cursor), limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a *v1api) GetKVValue(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName, params v1.GetKVValueParams) {
	w.Header().Set("Cache-Control", "no-store")
	v, err := a.s.M.GetKV(r.Context(), id, workload, name, params.Key)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *v1api) PutKVValue(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName, params v1.PutKVValueParams) {
	var body KVPut
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	k, err := a.s.M.PutKV(r.Context(), id, workload, name, params.Key, principalOf(r.Context()).Actor(r), body)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, k)
}

func (a *v1api) DeleteKVValue(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName, params v1.DeleteKVValueParams) {
	if err := a.s.M.DeleteKV(r.Context(), id, workload, name, params.Key, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}
