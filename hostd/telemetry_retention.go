package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// Request record retention (issue #120). Each workload's request log is
// bounded by size already (RecordFileMax, one previous file kept). With
// TelemetryOptions.RecordMaxAge (tw-hostd -request-log-max-age) the host
// also drops records older than that, from every tenant's request logs,
// open or not (a workload that stopped keeps its log until then).

// RecordPruneEvery is how often request logs are pruned by age.
const RecordPruneEvery = 10 * time.Minute

func (t *telemetry) startRecordPruner() {
	if t.opt.RecordMaxAge <= 0 {
		return
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		tick := time.NewTicker(min(RecordPruneEvery, t.opt.RecordMaxAge))
		defer tick.Stop()
		for {
			t.pruneRecords(time.Now())
			select {
			case <-t.stop:
				return
			case <-tick.C:
			}
		}
	}()
}

// pruneRecords drops the records older than RecordMaxAge before now, and
// returns how many.
func (t *telemetry) pruneRecords(now time.Time) int {
	cutoff := now.Add(-t.opt.RecordMaxAge)
	paths, _ := filepath.Glob(filepath.Join(t.h.cfg.DataDir, "tenants", "*", "logs", "*.requests.jsonl"))
	rotated, _ := filepath.Glob(filepath.Join(t.h.cfg.DataDir, "tenants", "*", "logs", "*.requests.jsonl.1"))
	for _, p := range rotated {
		cur := strings.TrimSuffix(p, ".1")
		if _, err := os.Stat(cur); os.IsNotExist(err) {
			paths = append(paths, cur) // only a predecessor is left
		}
	}
	total := 0
	for _, p := range paths {
		// t.mu: recordLog opens a workload's log under it, so a closed
		// log isn't opened while its file is cut.
		t.mu.Lock()
		var open *obs.RotatingFile
		for _, l := range t.logs {
			if l.f.Path() == p {
				open = l.f
			}
		}
		var n int
		var err error
		if open != nil {
			t.mu.Unlock()
			n, err = open.DropBefore(cutoff, obs.RecordTime)
		} else {
			var m int
			n, err = obs.DropBeforeFile(p+".1", cutoff, obs.RecordTime)
			if err == nil {
				m, err = obs.DropBeforeFile(p, cutoff, obs.RecordTime)
			}
			n += m
			t.mu.Unlock()
		}
		if err != nil {
			t.h.log.Warn("request log retention", "path", p, "err", err)
		}
		total += n
	}
	return total
}
