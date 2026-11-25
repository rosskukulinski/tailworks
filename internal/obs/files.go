package obs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RotatingFile is an append-only file capped at Max bytes: when a write
// would pass the cap, the file is renamed to <path>.1 (replacing the
// previous one) and a new file is started. At most 2×Max bytes are on
// disk. Readers that follow the file notice the rotation by its identity
// (os.SameFile).
type RotatingFile struct {
	path string
	max  int64
	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotating opens (appending to) path, creating it 0600.
func OpenRotating(path string, max int64) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, max: max}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Path is the current file.
func (r *RotatingFile) Path() string { return r.path }

// Write appends p, rotating first if needed. A single write larger than
// Max is truncated to Max.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	n := len(p)
	if int64(len(p)) > r.max {
		p = p[:r.max]
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.f.Close()
		os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			r.f = nil
			return 0, err
		}
	}
	k, err := r.f.Write(p)
	r.size += int64(k)
	if err != nil {
		return k, err
	}
	return n, nil
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Limiter is a token bucket: Allow takes one token if there is one.
type Limiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

// NewLimiter returns a full bucket.
func NewLimiter(rate, burst float64) *Limiter {
	return &Limiter{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

// Allow takes a token at now.
func (l *Limiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := now.Sub(l.last).Seconds(); d > 0 {
		l.tokens = min(l.burst, l.tokens+d*l.rate)
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// MaxScanBytes bounds how much of a record log one read scans (both
// files of a RotatingFile together).
const MaxScanBytes = 32 << 20

// TailRecords returns the last n records of the record log at path (and
// its rotated predecessor) that pass f, oldest first, and the size of the
// current file (where a follow continues).
func TailRecords(path string, n int, f Filter) ([]*Record, int64, error) {
	cur, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close()
	st, err := cur.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	if n <= 0 {
		return nil, size, nil
	}
	ring := make([]*Record, 0, min(n, 1024))
	push := func(r *Record) {
		if len(ring) == n {
			copy(ring, ring[1:])
			ring = ring[:n-1]
		}
		ring = append(ring, r)
	}
	scan := func(rd io.Reader, limit int64) {
		sc := bufio.NewScanner(io.LimitReader(rd, limit))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if f.Match(&r) {
				push(&r)
			}
		}
	}
	budget := int64(MaxScanBytes)
	if size < budget {
		if prev, err := os.Open(path + ".1"); err == nil {
			pst, _ := prev.Stat()
			skip := int64(0)
			if pst != nil && pst.Size() > budget-size {
				skip = pst.Size() - (budget - size)
			}
			if skip > 0 {
				prev.Seek(skip, io.SeekStart)
				skipLine(prev)
			}
			scan(prev, budget-size)
			prev.Close()
		}
	} else {
		cur.Seek(size-budget, io.SeekStart)
		skipLine(cur)
	}
	scan(cur, size)
	return ring, size, nil
}

func skipLine(f *os.File) {
	b := make([]byte, 1)
	for {
		if _, err := f.Read(b); err != nil || b[0] == '\n' {
			return
		}
	}
}

// RecordFollower reads records appended to a record log after an offset,
// across rotations.
type RecordFollower struct {
	Path string
	f    *os.File
	pos  int64
	part []byte
}

// NewRecordFollower continues path from offset pos.
func NewRecordFollower(path string, pos int64) *RecordFollower {
	return &RecordFollower{Path: path, pos: pos}
}

// Next returns the complete records appended since the last call.
func (rf *RecordFollower) Next() ([]*Record, error) {
	if rf.f == nil {
		f, err := os.Open(rf.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		rf.f = f
	}
	var out []*Record
	read := func() {
		buf := make([]byte, 64<<10)
		for {
			k, err := rf.f.ReadAt(buf, rf.pos)
			if k > 0 {
				rf.pos += int64(k)
				rf.part = append(rf.part, buf[:k]...)
				for {
					i := bytes.IndexByte(rf.part, '\n')
					if i < 0 {
						break
					}
					var r Record
					if json.Unmarshal(rf.part[:i], &r) == nil {
						out = append(out, &r)
					}
					rf.part = rf.part[i+1:]
				}
				if len(rf.part) > 1<<20 {
					rf.part = nil // a runaway line: drop it
				}
			}
			if err != nil || k < len(buf) {
				return
			}
		}
	}
	read()
	// Rotated (the path is a new file) or truncated: finish, then restart.
	if st, err := os.Stat(rf.Path); err == nil {
		fst, ferr := rf.f.Stat()
		if ferr == nil && (!os.SameFile(st, fst) || fst.Size() < rf.pos) {
			if os.SameFile(st, fst) {
				rf.pos = 0 // truncated in place
			} else {
				rf.f.Close()
				rf.f, rf.pos, rf.part = nil, 0, nil
				if f, err := os.Open(rf.Path); err == nil {
					rf.f = f
					read()
				}
			}
		}
	}
	return out, nil
}

// Close closes the follower.
func (rf *RecordFollower) Close() {
	if rf.f != nil {
		rf.f.Close()
	}
}
