package hostd

import (
	"log/slog"
	"testing"
)

// TestCronLastSurvivesRestart (#594, found by the e2e/dataplane cron loop's
// clock faults): which minutes already ran must survive a hostd restart.
// Otherwise a wall clock stepped back across the restart runs them again.
func TestCronLastSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	restart := func() *Host {
		h, _ := cronHost("* * * * *")
		h.cronLasts = openCronLast(dir, slog.New(slog.DiscardHandler))
		return h
	}
	h := restart()
	if n := names(h.cronDue(at("2027-01-04 00:05:10"))); n != "w:* * * * *@00:05" {
		t.Fatalf("first run: %q", n)
	}

	// hostd restarts; the clock has been stepped back two minutes.
	h = restart()
	for _, now := range []string{"2027-01-04 00:03:10", "2027-01-04 00:04:10", "2027-01-04 00:05:10"} {
		if rs := h.cronDue(at(now)); len(rs) != 0 {
			t.Fatalf("after a restart, at %s: reran %s", now, names(rs))
		}
	}
	if n := names(h.cronDue(at("2027-01-04 00:06:10"))); n != "w:* * * * *@00:06" {
		t.Fatalf("the next new minute: %q", n)
	}
}

// TestCronLastFarAheadIgnored: a minute stored while the clock was far
// fast (it has since been corrected) doesn't hold the triggers back.
func TestCronLastFarAheadIgnored(t *testing.T) {
	dir := t.TempDir()
	s := openCronLast(dir, slog.New(slog.DiscardHandler))
	s.save("tn/w", at("2027-01-04 03:00:00"))
	h, _ := cronHost("* * * * *")
	h.cronLasts = openCronLast(dir, slog.New(slog.DiscardHandler))
	if n := names(h.cronDue(at("2027-01-04 00:05:10"))); n != "w:* * * * *@00:05" {
		t.Fatalf("a stored minute 3 h ahead held the trigger: %q", n)
	}
	// Within the bound (a modest step back), it does hold.
	s.save("tn/w", at("2027-01-04 00:10:00"))
	h, _ = cronHost("* * * * *")
	h.cronLasts = openCronLast(dir, slog.New(slog.DiscardHandler))
	if rs := h.cronDue(at("2027-01-04 00:05:10")); len(rs) != 0 {
		t.Fatalf("a minute 5 min ahead must hold: ran %s", names(rs))
	}
}
