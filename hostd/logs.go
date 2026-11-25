package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// Workload logs over the push API (mgmt → hostd): GET /v1/logs serves the
// tail of one workload's app log (workerd.log, the node tier's app.log, or
// a microVM's serial console), and with follow=1 streams what is appended.
// mgmt calls it for `tw logs <workload>` from a linked machine
// (GET /v1/tenants/{id}/workloads/{workload}/logs).
//
// The file is always resolved here from the host's own record of the
// tenant's workload: the request names a tenant and a workload, never a
// path, so no input can reach another file.

const (
	// MaxLogLines caps n (the mgmt API's maximum).
	MaxLogLines = 10000
	// maxLogTailBytes bounds how far back a tail reads (very long lines).
	maxLogTailBytes = 16 << 20
	// maxLogFollow bounds one follow stream; the client follows again.
	maxLogFollow = time.Hour
	// logPollEvery is how often a follow stream checks the file.
	logPollEvery = 250 * time.Millisecond
	// logResolveEvery is how often a follow stream re-resolves the file
	// (a redeploy starts a new instance with a new log).
	logResolveEvery = 2 * time.Second
)

// logWorkloadRe is the mgmt API's workloadName (a DNS label). With
// tenantIDRE it is checked before the lookup, so "a" + "b/c" can never be
// confused with "a/b" + "c".
var logWorkloadRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// errNoWorkload: the tenant runs no such workload on this host.
var errNoWorkload = errors.New("no running workload")

// WorkloadLogFile returns the app log of tenantID's workload on this host.
func (h *Host) WorkloadLogFile(tenantID, workload string) (string, error) {
	if !tenantIDRE.MatchString(tenantID) || strings.Contains(tenantID, "..") || !logWorkloadRe.MatchString(workload) {
		return "", errNoWorkload
	}
	h.mu.Lock()
	w := h.workloads[tenantID+"/"+workload]
	h.mu.Unlock()
	if w == nil || w.tenantID != tenantID || w.name != workload {
		return "", errNoWorkload
	}
	inst := w.current()
	if inst == nil {
		return "", errNoWorkload
	}
	return inst.logFile(), nil
}

// logFile is the instance's app log; WorkloadStatus.LogFile reports the
// same file.
func (inst *instance) logFile() string {
	p := filepath.Join(inst.dir, "workerd.log")
	if inst.node != nil {
		p = inst.node.logPath
	}
	if vm := inst.currentVM(); vm != nil {
		if c := vm.Info().Console; c != "" {
			p = c // guest workerd logs to the serial console
		}
	}
	return p
}

// logsHandler is GET /v1/logs?tenant=&workload=&n=&follow=1.
func (h *Host) logsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantID, workload := q.Get("tenant"), q.Get("workload")
	n := 50
	if s := q.Get("n"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 || v > MaxLogLines {
			writeLogsError(w, 400, "bad_request", "n must be 0.."+strconv.Itoa(MaxLogLines))
			return
		}
		n = v
	}
	follow := q.Get("follow") == "1" || q.Get("follow") == "true"
	if q.Get("source") == "requests" {
		h.requestLogsHandler(w, r, tenantID, workload, n, follow) // logs_requests.go
		return
	}
	resolve := func() (string, error) { return h.WorkloadLogFile(tenantID, workload) }
	path, err := resolve()
	if err != nil {
		writeLogsError(w, 404, "unknown_workload", "no running workload "+workload+" of tenant "+tenantID+" on host "+h.cfg.HostID)
		return
	}
	ctx := r.Context()
	if follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxLogFollow)
		defer cancel()
	}
	if err := serveLog(ctx, w, path, n, follow, resolve); err != nil {
		writeLogsError(w, 404, "no_logs", "no log yet for workload "+workload+" on host "+h.cfg.HostID)
	}
}

// serveLog writes the last n lines of path and, with follow, what is
// appended to it (and to the file resolve returns, should it change) until
// ctx ends or resolve fails. It returns an error only before the first
// byte is written (the file can't be opened).
func serveLog(ctx context.Context, w http.ResponseWriter, path string, n int, follow bool, resolve func() (string, error)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()
	tail, pos, err := tailLines(f, n, maxLogTailBytes)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	rc := http.NewResponseController(w)
	if _, err := w.Write(tail); err != nil || !follow {
		return nil
	}
	rc.Flush()
	buf := make([]byte, 64<<10)
	tick := time.NewTicker(logPollEvery)
	defer tick.Stop()
	lastResolve := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if time.Since(lastResolve) >= logResolveEvery {
			lastResolve = time.Now()
			np, err := resolve()
			if err != nil {
				return nil // undeployed: the stream ends
			}
			if np == path && rotated(f, path) {
				np = "" // the capped log rotated under us: reopen the same path
			}
			if np != path {
				if np == "" {
					np = path
				}
				// A new instance: finish the old file, continue with the new one.
				if err := copyFrom(ctx, w, rc, f, &pos, buf); err != nil {
					return nil
				}
				nf, err := os.Open(np)
				if err != nil {
					continue // not created yet
				}
				f.Close()
				f, path, pos = nf, np, 0
			}
		}
		if st, err := f.Stat(); err == nil && st.Size() < pos {
			pos = 0 // truncated: start over
		}
		if err := copyFrom(ctx, w, rc, f, &pos, buf); err != nil {
			return nil
		}
	}
}

// copyFrom writes f from *pos to its current end and flushes.
func copyFrom(ctx context.Context, w io.Writer, rc *http.ResponseController, f *os.File, pos *int64, buf []byte) error {
	wrote := false
	for ctx.Err() == nil {
		k, err := f.ReadAt(buf, *pos)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return werr
			}
			*pos += int64(k)
			wrote = true
		}
		if err != nil || k < len(buf) {
			break
		}
	}
	if wrote {
		rc.Flush()
	}
	return ctx.Err()
}

// tailLines returns the last n lines of f (each ending in "\n"), reading at
// most maxBytes back from the end, and the offset the tail ends at.
func tailLines(f *os.File, n int, maxBytes int64) ([]byte, int64, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	if n <= 0 || size == 0 {
		return nil, size, nil
	}
	const chunk = 64 << 10
	var buf []byte
	off := size
	for off > 0 && size-off < maxBytes {
		k := min(int64(chunk), off, maxBytes-(size-off))
		off -= k
		b := make([]byte, k)
		if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, err
		}
		buf = append(b, buf...)
		if bytes.Count(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n")) >= n {
			break
		}
	}
	body := bytes.TrimSuffix(buf, []byte("\n"))
	idx := len(body)
	for i := 0; i < n; i++ {
		j := bytes.LastIndexByte(body[:idx], '\n')
		if j < 0 {
			idx = -1
			break
		}
		idx = j
	}
	out := append([]byte{}, body[idx+1:]...)
	return append(out, '\n'), size, nil
}

// writeLogsError is the typed error body (writeAPIError's shape) with an
// explicit status.
func writeLogsError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set(netd.HeaderError, code)
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "doc_url": netd.DocBase + code})
}
