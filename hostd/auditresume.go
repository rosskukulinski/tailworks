package hostd

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Continuing the audit chain across a restart (#377).
//
// The chain link (auditLog.prev) lives in memory, so before this every
// hostd restart began a new segment at prevHash "": mgmt stores it (it never
// splices a restart into history) and VerifyChain reports the host chain
// BROKEN at the first record after the restart, though nothing was lost or
// tampered with. `tw down [--purge]` + `tw up` on the same state dir hit it
// every time.
//
// On a clean shutdown with every chained record flushed, Close writes the
// last chained hash to auditHeadFile. The next start reads and removes it,
// and links to it only if mgmt's stored head (RegisterHost's auditHead) is
// that same hash and nothing has been chained yet in this run. Anything
// else starts a new segment as before, so verification still reports:
//   - records lost in a crash or an unclean exit (no file is written, or a
//     stale one was removed at the previous start);
//   - records still queued at shutdown (mgmt unreachable): no file;
//   - a chain truncated or rewritten at mgmt (heads differ).
//
// The file only says which hash to link to; mgmt still checks every link
// and hostSig at ingest, and a host key holder could forge records anyway.
const auditHeadFile = "audit-head"

// loadResume reads and removes the head a clean shutdown left. A file left
// in place could be reused after a later crash that lost records.
func (a *auditLog) loadResume(path string, log *slog.Logger) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if rmErr := os.Remove(path); rmErr != nil && log != nil {
		log.Warn("audit: removing the saved chain head failed", "err", rmErr)
	}
	if err != nil {
		return
	}
	a.mu.Lock()
	a.resumeFrom = strings.TrimSpace(string(b))
	a.mu.Unlock()
}

// resume links this run's chain to mgmtHead when it is the head the last
// clean shutdown saved. One-shot: only the first registration of a run
// counts. It reports whether the chain was continued.
func (a *auditLog) resume(mgmtHead string, log *slog.Logger) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := a.resumeFrom
	a.resumeFrom = ""
	switch {
	case saved == "":
		return false
	case a.prev != "":
		// Something was chained before registration: it already starts a
		// new segment, which verification reports.
		return false
	case mgmtHead != saved:
		// mgmtHead "": an empty chain (a new mgmt store), where a new
		// segment is simply the start, or a mgmt from before auditHead.
		if log != nil && mgmtHead != "" {
			log.Warn("audit: mgmt's head of this host's chain is not the last record this host flushed; starting a new chain segment (tw audit reports the break)",
				"host", saved, "mgmt", mgmtHead)
		}
		return false
	}
	a.prev = saved
	return true
}

// saveHead writes the chain head for the next start when every chained
// record has reached mgmt; otherwise it makes sure no head is left.
func (a *auditLog) saveHead(path string) error {
	a.mu.Lock()
	head, clean := a.prev, a.drops.n == 0 && len(a.pending) == 0 && len(a.tail) == 0 && (a.spill == nil || a.spill.count == 0)
	a.mu.Unlock()
	if head == "" || !clean {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".audit-head-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(head + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
