package obs

import (
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// Age-based retention for record logs (issue #120). A record log is
// already bounded by size (RotatingFile); with a max age the host also
// drops records older than it. Records are appended in time order, so
// dropping is cutting a prefix: everything before the first record that
// is at or after the cutoff. A line whose time can't be read stops the
// cut, so nothing after it is lost to a bad line.

// RecordTime reads a request record line's "time".
func RecordTime(line []byte) (time.Time, bool) {
	var r struct {
		Time time.Time `json:"time"`
	}
	if json.Unmarshal(line, &r) != nil || r.Time.IsZero() {
		return time.Time{}, false
	}
	return r.Time, true
}

// cutBefore returns the offset of the first line of data that is at or
// after cutoff (len(data) if every line is older), and how many lines
// come before it.
func cutBefore(data []byte, cutoff time.Time, at func([]byte) (time.Time, bool)) (int, int) {
	off, n := 0, 0
	for off < len(data) {
		end := bytes.IndexByte(data[off:], '\n')
		line := data[off:]
		if end >= 0 {
			line = data[off : off+end]
		}
		if len(bytes.TrimSpace(line)) > 0 {
			ts, ok := at(line)
			if !ok || !ts.Before(cutoff) {
				return off, n
			}
			n++
		}
		if end < 0 {
			return len(data), n
		}
		off += end + 1
	}
	return off, n
}

// DropBeforeFile cuts the records older than cutoff from the file at
// path (not open for writing: see RotatingFile.DropBefore), removing it
// if none is left. It returns how many records it dropped.
func DropBeforeFile(path string, cutoff time.Time, at func([]byte) (time.Time, bool)) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	off, n := cutBefore(data, cutoff, at)
	switch {
	case n == 0:
		return 0, nil
	case off >= len(data):
		return n, os.Remove(path)
	}
	tmp := path + ".prune"
	if err := os.WriteFile(tmp, data[off:], 0o600); err != nil {
		return 0, err
	}
	return n, os.Rename(tmp, path)
}

// DropBefore cuts the records older than cutoff from the file and its
// rotated predecessor. Writes wait while it runs (the files are at most
// Max bytes each).
func (r *RotatingFile) DropBefore(cutoff time.Time, at func([]byte) (time.Time, bool)) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := DropBeforeFile(r.path+".1", cutoff, at)
	if err != nil || r.f == nil {
		return n, err
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return n, err
	}
	off, m := cutBefore(data, cutoff, at)
	if m == 0 {
		return n, nil
	}
	tmp := r.path + ".prune"
	if err := os.WriteFile(tmp, data[off:], 0o600); err != nil {
		return n, err
	}
	r.f.Close()
	rerr := os.Rename(tmp, r.path)
	if rerr != nil {
		os.Remove(tmp)
		m = 0
	}
	if err := r.open(); err != nil {
		r.f = nil
		return n + m, err
	}
	return n + m, rerr
}
