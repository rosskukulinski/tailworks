package main

import (
	"strings"
	"testing"
	"time"
)

func TestWorkerdAgeCheck(t *testing.T) {
	t.Parallel()
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, c := range []struct {
		name   string
		pin    string
		now    time.Time
		status string
		detail string // a substring
		fix    bool
	}{
		{"same day", "1.20261121.1", day("2026-11-21"), "ok", "released 2026-11-21, 0 days old", false},
		{"one day", "1.20261121.1", day("2026-11-22"), "ok", ", 1 day old", false},
		{"three days", "1.20261121.1", day("2026-11-24").Add(13 * time.Hour), "ok", ", 3 days old", false},
		{"at the limit", "1.20261121.1", day("2026-12-21"), "ok", ", 30 days old", false},
		{"past the limit", "1.20261121.1", day("2026-12-22"), "warn", ", 31 days old; V8 security fixes", true},
		{"months", "1.20260901.2", day("2026-11-24"), "warn", ", 84 days old", true},
		{"clock behind the pin", "1.20261121.1", day("2026-11-10"), "ok", ", 0 days old", false},
		{"not a workerd version", "workerd 2026-11-21", day("2026-11-24"), "info", "is not a workerd version", false},
		{"bad date", "1.20261399.1", day("2026-11-24"), "info", "is not a workerd version", false},
	} {
		st, d, fix := workerdAgeCheck(c.pin, c.now)
		if st != c.status || !strings.Contains(d, c.detail) || (fix != "") != c.fix {
			t.Errorf("%s: got %s %q fix=%q; want %s containing %q, fix=%v", c.name, st, d, fix, c.status, c.detail, c.fix)
		}
		if st != "info" && !strings.HasPrefix(d, "pinned workerd "+c.pin+" was released ") {
			t.Errorf("%s: detail %q", c.name, d)
		}
	}
	// The summary line's fact is the age (doctor_render.go).
	if f := metaOf("workerd_age").fact("pinned workerd 1.20261121.1 was released 2026-11-21, 3 days old"); f != "3 days" {
		t.Errorf("fact: %q", f)
	}
}
