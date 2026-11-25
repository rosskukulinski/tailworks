// Package kvwire is the Workers KV store kind's journal entry format and
// limits (ADR-037). The platform's KV module (hostd/gen/js/kv.js)
// writes these entries to a platform store's journal
// (ADR-044); mgmt's admin KV view and `tw kv` read and write
// the same entries. The two encoders must agree byte for byte: a change
// here is a change there (kvwire_test.go pins the format).
//
// An entry is
//
//	0x01 | uint32 big-endian header length | header JSON | value bytes
//
// where the header is {"k": key, "x": expiration (Unix seconds, optional),
// "m": metadata as JSON text (optional), "d": true for a delete}. A delete
// has no value. The entry's compaction key is the KV key, so a later put or
// delete of the key replaces every earlier entry for it.
package kvwire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Kind is the KV store kind (the API's {kind}).
const Kind = "kv"

// Limits. Cloudflare's published limits, except MaxValueBytes: a value is
// one journal entry, and a platform store entry is at most 2 MiB
// (mgmt.StoreEntryMaxBytes), so the value gets 2 MiB minus room for the
// header.
const (
	MaxKeyBytes      = 512
	MaxMetadataBytes = 1024
	MaxValueBytes    = 2<<20 - 4096
	MaxListKeys      = 1000
	MaxBulkKeys      = 100
	// MinTTLSeconds is the smallest expirationTtl, and how far in the
	// future an absolute expiration must be (as on Cloudflare).
	MinTTLSeconds = 60
)

const version = 1

// Header is an entry's header.
type Header struct {
	Key        string `json:"k"`
	Expiration int64  `json:"x,omitempty"` // Unix seconds; 0: never
	Metadata   string `json:"m,omitempty"` // JSON text; "": none
	Delete     bool   `json:"d,omitempty"`
}

// Entry is one decoded journal entry.
type Entry struct {
	Header
	Value []byte
}

// Encode encodes an entry.
func Encode(h Header, value []byte) []byte {
	hj, _ := json.Marshal(h)
	out := make([]byte, 0, 5+len(hj)+len(value))
	out = append(out, version)
	out = binary.BigEndian.AppendUint32(out, uint32(len(hj)))
	out = append(out, hj...)
	return append(out, value...)
}

// Decode decodes an entry.
func Decode(b []byte) (Entry, error) {
	if len(b) >= 1 && b[0] > version {
		return Entry{}, fmt.Errorf("kvwire: entry v%d was written by a newer tailworks (this one reads v%d); upgrade tw", b[0], version)
	}
	if len(b) < 5 || b[0] != version {
		return Entry{}, errors.New("kvwire: not a v1 KV entry")
	}
	n := binary.BigEndian.Uint32(b[1:5])
	if uint64(n) > uint64(len(b)-5) {
		return Entry{}, errors.New("kvwire: truncated KV entry")
	}
	var e Entry
	if err := json.Unmarshal(b[5:5+n], &e.Header); err != nil {
		return Entry{}, fmt.Errorf("kvwire: header: %w", err)
	}
	e.Value = b[5+n:]
	return e, nil
}

// CheckKey checks a key against Cloudflare's rules.
func CheckKey(k string) error {
	switch {
	case k == "":
		return errors.New("key names must not be empty")
	case k == "." || k == "..":
		return fmt.Errorf("illegal key name %q", k)
	case len(k) > MaxKeyBytes:
		return fmt.Errorf("UTF-8 encoded length of %d exceeds key length limit of %d", len(k), MaxKeyBytes)
	case !utf8.ValidString(k):
		return errors.New("key names must be UTF-8")
	}
	return nil
}

// CheckMetadata checks metadata JSON text.
func CheckMetadata(m string) error {
	if len(m) > MaxMetadataBytes {
		return fmt.Errorf("metadata length of %d exceeds limit of %d", len(m), MaxMetadataBytes)
	}
	if m != "" && !json.Valid([]byte(m)) {
		return errors.New("metadata must be JSON")
	}
	return nil
}

// Item is a live key in a folded journal.
type Item struct {
	Key        string
	Value      []byte
	Expiration int64
	Metadata   string
}

// Fold replays decoded journal entry data (in seq order) into the live
// keys at now (Unix seconds): later entries replace earlier ones, deletes
// remove, and expired keys are left out.
func Fold(entries [][]byte, now int64) (map[string]Item, error) {
	out := map[string]Item{}
	for _, d := range entries {
		e, err := Decode(d)
		if err != nil {
			return nil, err
		}
		if e.Delete {
			delete(out, e.Key)
			continue
		}
		out[e.Key] = Item{Key: e.Key, Value: e.Value, Expiration: e.Expiration, Metadata: e.Metadata}
	}
	for k, it := range out {
		if it.Expiration != 0 && it.Expiration <= now {
			delete(out, k)
		}
	}
	return out, nil
}

// Page lists folded keys like KV's list(): sorted by UTF-8 bytes, those
// starting with prefix and after cursor (the last key of the previous
// page), at most limit. next is the cursor of the following page ("" at
// the end).
func Page(items map[string]Item, prefix, cursor string, limit int) (page []Item, next string) {
	keys := make([]string, 0, len(items))
	for k := range items {
		if strings.HasPrefix(k, prefix) && k > cursor {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit <= 0 || limit > MaxListKeys {
		limit = MaxListKeys
	}
	if len(keys) > limit {
		keys, next = keys[:limit], keys[limit-1]
	}
	for _, k := range keys {
		page = append(page, items[k])
	}
	return page, next
}
