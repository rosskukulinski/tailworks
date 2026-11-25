// Package catalog is tailworks' release catalog (ADR-041 §8):
// for every release, its notes and the facts an upgrade plan needs (the
// workerd pin, the newest mgmt migration, contract migrations, protocol
// versions). One JSON file per release lives in releases/, written by
// `scripts/release-notes.sh --finalize`, and is embedded in every binary
// that imports this package (tw-mgmt), so planning works offline.
package catalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/version"
)

// Note is one release-note fragment (changes/<branch>.md).
type Note struct {
	Kind    string   `json:"kind"`
	Upgrade []string `json:"upgrade,omitempty"`
	Source  string   `json:"source"` // the fragment's file name, e.g. "my-branch.md"
	Text    string   `json:"text"`   // its markdown body
}

// Entry is one release.
type Entry struct {
	Version            string         `json:"version"` // "0.2.0"
	Date               string         `json:"date"`    // YYYY-MM-DD
	Workerd            string         `json:"workerd"`
	Schema             string         `json:"schema,omitempty"` // newest mgmt migration ID
	ContractMigrations []string       `json:"contractMigrations,omitempty"`
	Protocols          map[string]int `json:"protocols,omitempty"`
	Upgrade            []string       `json:"upgrade,omitempty"` // union of the notes' upgrade kinds
	Notes              []Note         `json:"notes"`
}

//go:embed releases/*.json
var releases embed.FS

// Embedded returns every embedded release, ascending by version. A
// malformed file is a build-time mistake: it panics (the unit test catches
// it before a release does).
func Embedded() []Entry {
	var out []Entry
	files, err := fs.Glob(releases, "releases/*.json")
	if err != nil {
		panic(err)
	}
	for _, f := range files {
		b, err := releases.ReadFile(f)
		if err != nil {
			panic(err)
		}
		es, err := Parse(b)
		if err != nil {
			panic(fmt.Sprintf("catalog: %s: %v", f, err))
		}
		out = append(out, es...)
	}
	Sort(out)
	return out
}

// Parse decodes one Entry object or an array of them. Every entry must
// have a valid version.
func Parse(b []byte) ([]Entry, error) {
	b = bytes.TrimSpace(b)
	var es []Entry
	if len(b) > 0 && b[0] == '[' {
		if err := json.Unmarshal(b, &es); err != nil {
			return nil, err
		}
	} else {
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		es = []Entry{e}
	}
	for _, e := range es {
		if _, err := version.Parse(e.Version); err != nil {
			return nil, fmt.Errorf("catalog entry: %w", err)
		}
	}
	return es, nil
}

// Sort orders entries ascending by version (unparseable versions last).
func Sort(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool { return cmp(es[i].Version, es[j].Version) < 0 })
}

func cmp(a, b string) int {
	va, ea := version.Parse(a)
	vb, eb := version.Parse(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return 1
	case eb != nil:
		return -1
	}
	return va.Compare(vb)
}

// Between returns the entries with from < version <= to, ascending: what an
// upgrade from `from` to `to` passes through. An empty from means "from
// the beginning".
func Between(es []Entry, from, to string) []Entry {
	var out []Entry
	for _, e := range es {
		if (from == "" || cmp(e.Version, from) > 0) && cmp(e.Version, to) <= 0 {
			out = append(out, e)
		}
	}
	Sort(out)
	return out
}

// Find returns the entry for v (ok=false if absent).
func Find(es []Entry, v string) (Entry, bool) {
	for _, e := range es {
		if cmp(e.Version, v) == 0 {
			return e, true
		}
	}
	return Entry{}, false
}

// Breaking returns every breaking note in es, in order.
func Breaking(es []Entry) []Note {
	var out []Note
	for _, e := range es {
		for _, n := range e.Notes {
			if n.Kind == KindBreaking {
				out = append(out, n)
			}
		}
	}
	return out
}

// ContractSteps returns the entries that carry contract migrations: an
// upgrade must stop at each of them (every replica at that version before
// its contract migration runs), so `tw upgrade` never jumps over one.
func ContractSteps(es []Entry) []Entry {
	var out []Entry
	for _, e := range es {
		if len(e.ContractMigrations) > 0 {
			out = append(out, e)
		}
	}
	return out
}
