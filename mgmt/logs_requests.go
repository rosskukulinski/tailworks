package mgmt

import (
	"github.com/rosskukulinski/tailworks/internal/obs"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// logsSource reads GET …/logs' source and record filters into q
// (ADR-045). hostd applies the filters; mgmt only checks them.
func logsSource(q *LogsQuery, p v1.GetWorkloadLogsParams) error {
	if p.Source != nil {
		switch s := string(*p.Source); s {
		case "", "console":
		case "requests":
			q.Source = s
		default:
			return errf(400, "bad_request", "source must be console or requests")
		}
	}
	f := obs.Filter{Object: str(p.Object), Status: str(p.Status), Outcome: str(p.Outcome), Caller: str(p.Caller), Trace: str(p.Trace), Errors: p.Errors != nil && *p.Errors}
	if f.Empty() {
		return nil
	}
	if q.Source != "requests" {
		return errf(400, "bad_request", "the filters (object, status, outcome, caller, trace, errors) need source=requests")
	}
	if _, err := obs.ParseFilter(f.Values()); err != nil {
		return errf(400, "bad_request", "%v", err)
	}
	q.Filter = f.Values()
	return nil
}

// logsContentType: records are JSON Lines, the console is text.
func logsContentType(source string) string {
	if source == "requests" {
		return "application/x-ndjson"
	}
	return "text/plain; charset=utf-8"
}
