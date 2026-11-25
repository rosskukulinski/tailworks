// Package r2wire reads the R2 store kind's index journal (ADR-036): the
// entries the platform's R2 module (hostd/gen/js/store-r2.js) appends to
// a bucket's platform store (ADR-044). mgmt folds them into a bucket's
// live objects for `GET …/stores/r2/{name}/objects` (`tw r2 object ls`).
// store-r2.js is the only writer; a change to its entries is a change
// here (r2wire_test.go replays the JS-checked fixtures in
// hostd/gen/testdata/formats).
//
// Each entry is JSON with a version v (absent: v1, written before
// versioning) and an op o:
//
//	{o:"put", k, row, up?}   ckey "o:"+k
//	{o:"del", k}             ckey "o:"+k
//	{o:"mpu", id, k, …}      ckey "u:"+id   (multipart upload state)
//	{o:"part", id, n, …}     ckey "p:"+id+":"+n
//	{o:"mpx", id}            ckey "u:"+id
//
// Only put and del change the live objects; the multipart entries are
// skipped. An entry with a newer v or an unknown op was written by a
// newer tailworks and is an error, as it is in store-r2.js: skipping it
// could list a deleted object.
package r2wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kind is the R2 store kind (the API's {kind}).
const Kind = "r2"

// Version is the newest index entry version this tailworks reads
// (store-r2.js's R2_ENTRY_V).
const Version = 1

// Object is one live object of a bucket, as the index describes it.
type Object struct {
	Key         string `json:"key"`
	Version     string `json:"version"`
	Size        int64  `json:"size"`
	ETag        string `json:"etag"`
	Uploaded    int64  `json:"uploaded"` // unix ms
	ContentType string `json:"contentType,omitempty"`
}

// ErrNewer is wrapped by Fold.Add's error for an entry written by a
// newer tailworks.
var ErrNewer = errors.New("r2wire: written by a newer tailworks")

type entry struct {
	V   *int   `json:"v"`
	O   string `json:"o"`
	K   string `json:"k"`
	Row struct {
		Version  string            `json:"version"`
		Size     int64             `json:"size"`
		ETag     string            `json:"etag"`
		Uploaded int64             `json:"uploaded"`
		HTTP     map[string]string `json:"http"`
	} `json:"row"`
}

// Fold replays index entries, in journal order, into the live objects
// whose keys start with Prefix and sort after After. Keys outside that
// window are never held, so a fold's memory is that of its answer.
type Fold struct {
	Prefix string
	After  string
	live   map[string]Object
}

// Add applies one entry.
func (f *Fold) Add(data []byte) error {
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("r2wire: entry: %w", err)
	}
	v := 1
	if e.V != nil {
		v = *e.V
	}
	if v > Version {
		return fmt.Errorf("%w: index entry v%d (this one reads up to v%d); upgrade tw-mgmt", ErrNewer, v, Version)
	}
	switch e.O {
	case "put", "del":
	case "mpu", "part", "mpx":
		return nil
	default:
		return fmt.Errorf("%w: unknown index entry op %q (v%d); upgrade tw-mgmt", ErrNewer, e.O, v)
	}
	if !strings.HasPrefix(e.K, f.Prefix) || e.K <= f.After {
		return nil
	}
	if f.live == nil {
		f.live = map[string]Object{}
	}
	if e.O == "del" {
		delete(f.live, e.K)
		return nil
	}
	f.live[e.K] = Object{Key: e.K, Version: e.Row.Version, Size: e.Row.Size, ETag: e.Row.ETag, Uploaded: e.Row.Uploaded, ContentType: e.Row.HTTP["contentType"]}
	return nil
}

// Objects returns the live objects in key order (UTF-8 bytes, as R2's
// list() sorts them).
func (f *Fold) Objects() []Object {
	out := make([]Object, 0, len(f.live))
	for _, o := range f.live {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
