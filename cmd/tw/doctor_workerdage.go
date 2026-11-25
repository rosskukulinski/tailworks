package main

import (
	"fmt"
	"strings"
	"time"
)

// tw doctor's workerd_age check (issue #338). V8 security fixes reach a host
// only inside a newer workerd, which reaches it only inside a newer tailworks
// release (docs/RELEASING.md "Upstream advisories"), so the age of the
// pinned workerd is how far behind the V8 fixes this host can be at most.
// workerd releases about weekly; a pin older than workerdMaxAgeDays has
// missed several, and the check warns.

const workerdMaxAgeDays = 30

// workerdAgeCheck is the check's result for pin (1.YYYYMMDD.N) at now.
func workerdAgeCheck(pin string, now time.Time) (status, detail, fix string) {
	date, err := workerdPinTime(pin)
	if err != nil {
		return "info", "pinned workerd " + pin + ": " + err.Error(), ""
	}
	days := int(now.UTC().Sub(date).Hours() / 24)
	if days < 0 {
		days = 0
	}
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	detail = fmt.Sprintf("pinned workerd %s was released %s, %d %s old", pin, date.Format("2006-01-02"), days, unit)
	if days > workerdMaxAgeDays {
		return "warn", detail + "; V8 security fixes reach this host only through a newer tailworks release", "upgrade when a release is out (tw version --check); see docs/RELEASING.md \"Upstream advisories\""
	}
	return "ok", detail, ""
}

// workerdPinTime is the release day in a workerd version (1.YYYYMMDD.N →
// YYYY-MM-DD, UTC).
func workerdPinTime(pin string) (time.Time, error) {
	parts := strings.Split(pin, ".")
	if len(parts) != 3 || len(parts[1]) != 8 {
		return time.Time{}, fmt.Errorf("%q is not a workerd version (1.YYYYMMDD.N)", pin)
	}
	t, err := time.Parse("20060102", parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a workerd version (1.YYYYMMDD.N): %v", pin, err)
	}
	return t, nil
}
