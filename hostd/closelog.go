package hostd

import (
	"log/slog"
	"time"
)

// closeLog times the phases of a shutdown and logs them in one line, so a
// slow stop shows where the time went (#245).
type closeLog struct {
	log    *slog.Logger
	t0, at time.Time
	attrs  []any
}

func newCloseLog(log *slog.Logger) *closeLog {
	now := time.Now()
	return &closeLog{log: log, t0: now, at: now}
}

// lap records the time since the previous lap as <phase>_ms.
func (c *closeLog) lap(phase string) {
	now := time.Now()
	c.attrs = append(c.attrs, phase+"_ms", now.Sub(c.at).Milliseconds())
	c.at = now
}

func (c *closeLog) done() {
	c.log.Info("host stopped", append(c.attrs, "total_ms", time.Since(c.t0).Milliseconds())...)
}
