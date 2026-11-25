package hostd

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// requestLogsHandler is GET /v1/logs?source=requests: the workload's
// structured request records (ADR-045) as JSON Lines, the last
// n that pass the filters, then (follow) new ones. Like the console, the
// file is resolved from the host's own record of the tenant's workload.
func (h *Host) requestLogsHandler(w http.ResponseWriter, r *http.Request, tenantID, workload string, n int, follow bool) {
	f, err := obs.ParseFilter(r.URL.Query())
	if err != nil {
		writeLogsError(w, 400, "bad_request", err.Error())
		return
	}
	if _, err := h.WorkloadLogFile(tenantID, workload); err != nil {
		writeLogsError(w, 404, "unknown_workload", "no running workload "+workload+" of tenant "+tenantID+" on host "+h.cfg.HostID)
		return
	}
	path := h.RequestLogPath(tenantID, workload)
	recs, pos, err := obs.TailRecords(path, n, f)
	if err != nil && !os.IsNotExist(err) {
		writeLogsError(w, 404, "no_logs", "no request records yet for workload "+workload+" on host "+h.cfg.HostID)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	for _, rec := range recs {
		if enc.Encode(rec) != nil {
			return
		}
	}
	rc.Flush()
	if !follow {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxLogFollow)
	defer cancel()
	fl := obs.NewRecordFollower(path, pos)
	defer fl.Close()
	tick := time.NewTicker(logPollEvery)
	defer tick.Stop()
	lastCheck := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if time.Since(lastCheck) >= logResolveEvery {
			lastCheck = time.Now()
			if _, err := h.WorkloadLogFile(tenantID, workload); err != nil {
				return // undeployed: the stream ends
			}
		}
		recs, err := fl.Next()
		if err != nil {
			return
		}
		wrote := false
		for _, rec := range recs {
			if !f.Match(rec) {
				continue
			}
			if enc.Encode(rec) != nil {
				return
			}
			wrote = true
		}
		if wrote {
			rc.Flush()
		}
	}
}

// rotated reports whether path is no longer the file f has open (a
// size-capped log rotated).
func rotated(f *os.File, path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	fst, err := f.Stat()
	return err == nil && !os.SameFile(st, fst)
}
