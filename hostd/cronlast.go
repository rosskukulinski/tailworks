package hostd

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cronLastStore keeps each workload's last evaluated cron minute
// (workload.cronLast) on disk, so a hostd restart doesn't forget which
// minutes already ran. Without it, a wall clock stepped back across a
// restart reran them (#594). <DataDir>/cron-last.json maps
// "<tenant>/<workload>" to the minute (Unix ms, UTC). It is written when a
// minute is marked, so at most once a minute per workload.
type cronLastStore struct {
	path string
	log  *slog.Logger
	mu   sync.Mutex
	m    map[string]int64
}

// cronLastAhead bounds how far ahead of the clock a stored minute may be.
// Further ahead, the clock was wrong (fast) before the restart and has
// since been corrected. Honouring the minute would hold every trigger of
// that workload until the clock caught up, so it is dropped (and logged).
const cronLastAhead = 15 * time.Minute

func openCronLast(dataDir string, log *slog.Logger) *cronLastStore {
	s := &cronLastStore{path: filepath.Join(dataDir, "cron-last.json"), log: log, m: map[string]int64{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.m); err != nil {
			log.Warn("cron-last.json unreadable; starting without it", "err", err)
			s.m = map[string]int64{}
		}
	}
	return s
}

// load is the stored minute for key, or zero (none, or too far ahead of now).
func (s *cronLastStore) load(key string, now time.Time) time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	ms, ok := s.m[key]
	s.mu.Unlock()
	if !ok {
		return time.Time{}
	}
	t := time.UnixMilli(ms).UTC()
	if t.After(now.Add(cronLastAhead)) {
		s.log.Warn("cron: the last minute that ran is far ahead of the clock (it was fast, and was corrected?); not holding triggers back for it",
			"workload", key, "lastMinute", t, "now", now.UTC())
		return time.Time{}
	}
	return t
}

// save records minute for key.
func (s *cronLastStore) save(key string, minute time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = minute.UnixMilli()
	b, _ := json.Marshal(s.m)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.log.Warn("cron-last.json", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.log.Warn("cron-last.json", "err", err)
	}
}
