package hostd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// auditSpill is the on-disk overflow of the audit queue (RT3-03 / H03):
// chained host records that arrive while maxPendingAudit records already
// wait in memory, one JSON record per line, in chain order. Flushed records
// are skipped by offset; the file is truncated once it is fully flushed.
//
// The chain link lives in memory, so records spilled by an earlier run
// cannot be linked to this run's chain: they are discarded at start and
// reported as audit_dropped{restart}.
type auditSpill struct {
	path    string
	f       *os.File
	readOff int64 // first unflushed byte
	size    int64
	count   int // unflushed records
	max     int64
}

// maxAuditSpillBytes caps the spill. Past it only public_request records
// are refused (counted in audit_dropped); every other host record is still
// written.
const maxAuditSpillBytes = 256 << 20

// openSpill attaches a spill file to the log.
func (a *auditLog) openSpill(path string, log *slog.Logger) error {
	left, err := countLines(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.spill = &auditSpill{path: path, f: f, max: maxAuditSpillBytes}
	if left > 0 {
		a.drops.count(dropRestart, left, a.now())
		if log != nil {
			log.Warn("audit records spilled by a previous run were not flushed; reported as audit_dropped", "records", left)
		}
	}
	return nil
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, buf := 0, make([]byte, 64<<10)
	for {
		k, err := f.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

func (s *auditSpill) full() bool { return s.size >= s.max }

func (s *auditSpill) append(r mgmtapi.AuditRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := s.f.Write(b); err != nil {
		// A short write leaves a partial line: cut it off so the next
		// record starts on its own line.
		s.f.Truncate(s.size)
		return err
	}
	s.size += int64(len(b))
	s.count++
	return nil
}

// read returns up to n unflushed records and the offset after them.
func (s *auditSpill) read(n int) ([]mgmtapi.AuditRecord, int64, error) {
	br := bufio.NewReader(io.NewSectionReader(s.f, s.readOff, s.size-s.readOff))
	off := s.readOff
	var recs []mgmtapi.AuditRecord
	for len(recs) < n {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var r mgmtapi.AuditRecord
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, 0, err
			}
			recs = append(recs, r)
			off += int64(len(line))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
	}
	return recs, off, nil
}

// commit marks k records (up to off) as flushed.
func (s *auditSpill) commit(off int64, k int) {
	s.readOff = off
	s.count -= k
	if s.count <= 0 {
		s.count, s.readOff, s.size = 0, 0, 0
		s.f.Truncate(0)
	}
}
