package mgmt

import (
	"fmt"
	"time"
)

// Bounds on what a host's path report can claim (security/LEDGER.md H23,
// #156). A host's report is its own word about its own paths: mgmt can't
// check the numbers, so it bounds them. A value past a bound can never make
// a host look better than an honest report would.

const (
	// maxPathRTT is the largest RTT stored. A larger report is clamped to
	// it (no real tailnet path is slower; the clamp only stops a huge value
	// from overflowing int64 microseconds into a negative, "best" RTT).
	maxPathRTT = time.Minute
	// maxPathAge is the largest age stored: older is stale anyway. The
	// clamp stops a huge ageMs from overflowing time.Duration into a
	// measuredAt in the future, which would never go stale and never be
	// deleted by pathRetention.
	maxPathAge = pathRetention
	// pathFutureSlack is how far in the future a stored measuredAt may be
	// before placement ignores it (rows written before these bounds, or by
	// a replica whose store clock read ahead).
	pathFutureSlack = time.Minute
)

// boundSample clamps a validated sample's RTT and age.
func boundSample(s PathSample) PathSample {
	s.RTTMs = min(s.RTTMs, float64(maxPathRTT/time.Millisecond))
	s.AgeMs = min(s.AgeMs, int64(maxPathAge/time.Millisecond))
	return s
}

// unusableMeasurement says why placement must not score m ("" = usable):
// an RTT outside [0, maxPathRTT] or a measuredAt in the future.
func unusableMeasurement(m PathMeasurement, now time.Time) string {
	switch {
	case m.RTTMicros < 0 || m.RTTMicros > int64(maxPathRTT/time.Microsecond):
		return fmt.Sprintf("bad RTT (%d µs)", m.RTTMicros)
	case time.UnixMilli(m.MeasuredAt).Sub(now) > pathFutureSlack:
		return "measured in the future"
	}
	return ""
}
