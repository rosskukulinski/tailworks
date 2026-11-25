package main

import (
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/mgmtd"
)

// Retention knobs of `tw up` (issue #120), saved in tw.json and passed to
// mgmt (audit records: mgmt/retention.go) and the host (request records:
// hostd/telemetry_retention.go). All are off by default: the audit log is
// kept forever and request records until the size rotation drops them.

type retentionFlags struct {
	auditAge   *time.Duration
	auditRecs  *int64
	requestAge *time.Duration
}

func addRetentionFlags(f *flags) retentionFlags {
	f.group(upGroupRetention,
		"audit-max-age",
		"audit-max-records",
		"request-log-max-age",
	)
	return retentionFlags{
		auditAge:   f.Duration("audit-max-age", 0, "audit retention: delete audit records older than this (e.g. 2160h), keeping a checkpoint so the rest still verify (0 = keep every record, the default)"),
		auditRecs:  f.Int64("audit-max-records", 0, "audit retention: keep only the newest N records of each audit chain (0 = keep every record, the default)"),
		requestAge: f.Duration("request-log-max-age", 0, "also drop request records (tw logs --source requests) older than this, e.g. 168h (0 = only the size rotation, the default)"),
	}
}

// save records the flags in tw.json.
func (r retentionFlags) save(c *Config) error {
	if *r.auditAge < 0 || *r.auditRecs < 0 || *r.requestAge < 0 {
		return fail("bad_request", "--audit-max-age, --audit-max-records and --request-log-max-age must be ≥ 0 (0 = off)")
	}
	c.AuditMaxAge, c.AuditMaxRecords, c.RequestLogMaxAge = "", *r.auditRecs, ""
	if *r.auditAge > 0 {
		c.AuditMaxAge = r.auditAge.String()
	}
	if *r.requestAge > 0 {
		c.RequestLogMaxAge = r.requestAge.String()
	}
	return nil
}

// keepSavedRetention is keepSavedUpFlags for these flags.
func keepSavedRetention(keep func(name, saved string), c Config) {
	keep("audit-max-age", c.AuditMaxAge)
	if c.AuditMaxRecords > 0 {
		keep("audit-max-records", strconv.FormatInt(c.AuditMaxRecords, 10))
	}
	keep("request-log-max-age", c.RequestLogMaxAge)
}

func (c Config) retentionMgmtArgs() []string {
	var a []string
	if c.AuditMaxAge != "" {
		a = append(a, "-audit-max-age", c.AuditMaxAge)
	}
	if c.AuditMaxRecords > 0 {
		a = append(a, "-audit-max-records", strconv.FormatInt(c.AuditMaxRecords, 10))
	}
	return a
}

func (c Config) retentionHostdArgs() []string {
	if c.RequestLogMaxAge == "" {
		return nil
	}
	return []string{"-request-log-max-age", c.RequestLogMaxAge}
}

// retentionSingle sets the knobs of single-process tw up.
func (c Config) retentionSingle(mo *mgmtd.Options, ho *hostdrun.Options) {
	if mo != nil {
		mo.AuditMaxAge, _ = time.ParseDuration(c.AuditMaxAge)
		mo.AuditMaxRecords = c.AuditMaxRecords
	}
	if ho != nil {
		ho.RequestLogMaxAge, _ = time.ParseDuration(c.RequestLogMaxAge)
	}
}
