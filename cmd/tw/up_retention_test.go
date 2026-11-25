package main

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/mgmtd"
)

// TestUpRetention (#120): tw up's retention flags are saved in tw.json,
// kept by a later tw up, and reach mgmt and the host in both layouts; off
// by default.
func TestUpRetention(t *testing.T) {
	var off Config
	if a := off.retentionMgmtArgs(); len(a) != 0 || off.retentionHostdArgs() != nil {
		t.Fatalf("default args: %v %v", a, off.retentionHostdArgs())
	}
	fs := upFlagSet(t)
	if err := fs.Parse([]string{"--audit-max-age", "2160h", "--audit-max-records", "100000", "--request-log-max-age", "168h"}); err != nil {
		t.Fatal(err)
	}
	f := &flags{FlagSet: fs}
	r := retentionFlags{auditAge: durFlag(t, f, "audit-max-age"), auditRecs: int64Flag(t, f, "audit-max-records"), requestAge: durFlag(t, f, "request-log-max-age")}
	var c Config
	if err := r.save(&c); err != nil || c.AuditMaxAge != "2160h0m0s" || c.AuditMaxRecords != 100000 || c.RequestLogMaxAge != "168h0m0s" {
		t.Fatalf("saved: %+v %v", c, err)
	}
	if a := c.retentionMgmtArgs(); !slices.Equal(a, []string{"-audit-max-age", "2160h0m0s", "-audit-max-records", "100000"}) {
		t.Fatalf("mgmt args: %v", a)
	}
	if a := c.retentionHostdArgs(); !slices.Equal(a, []string{"-request-log-max-age", "168h0m0s"}) {
		t.Fatalf("hostd args: %v", a)
	}
	var mo mgmtd.Options
	var ho hostdrun.Options
	c.retentionSingle(&mo, &ho)
	if mo.AuditMaxAge != 2160*time.Hour || mo.AuditMaxRecords != 100000 || ho.RequestLogMaxAge != 168*time.Hour {
		t.Fatalf("single process: %v %v %v", mo.AuditMaxAge, mo.AuditMaxRecords, ho.RequestLogMaxAge)
	}
	// A later tw up keeps them.
	fs = upFlagSet(t)
	keepSavedUpFlags(fs, c)
	for name, v := range map[string]string{"audit-max-age": "2160h0m0s", "audit-max-records": "100000", "request-log-max-age": "168h0m0s"} {
		if got := fs.Lookup(name).Value.String(); got != v {
			t.Errorf("--%s = %q, want the saved %q", name, got, v)
		}
	}
}

func durFlag(t *testing.T, f *flags, name string) *time.Duration {
	d, err := time.ParseDuration(f.Lookup(name).Value.String())
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func int64Flag(t *testing.T, f *flags, name string) *int64 {
	n, err := strconv.ParseInt(f.Lookup(name).Value.String(), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return &n
}
