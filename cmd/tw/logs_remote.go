package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// remoteLogsContext ends a follow on Ctrl-C (tests replace it).
var remoteLogsContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// remoteLogs is `tw logs <workload>` on a linked machine: the linked mgmt
// reads the workload's log from the host holding its lease
// (GET /v1/tenants/{id}/workloads/{workload}/logs). A workload with
// replicas prints every replica's lines prefixed "[<host>] "; --host picks
// one. The daemons' logs (--component) stay on the host.
func remoteLogs(out io.Writer, s *state, pos []string, comp, tenantRef, host string, n int, follow bool, lo logsOpts) error {
	if comp != "" || len(pos) != 1 {
		return fail("remote_unsupported", "the daemons' logs (--component mgmt|hostd|supervisor) are files on the host that runs tw up (this machine is linked to "+s.Link.Endpoint+")").
			withFix("tw logs <workload>   (a workload's log works from here)", "run `tw logs --component …` on the host")
	}
	if n < 0 || n > mgmt.MaxLogLines {
		return fail("bad_request", "-n must be 0.."+strconv.Itoa(mgmt.MaxLogLines)+" from a linked machine")
	}
	tn, err := s.tenant(tenantRef)
	if err != nil {
		return err
	}
	ctx, stop := remoteLogsContext()
	defer stop()
	c := s.mgmt()
	opts := mgmtapi.LogsOptions{N: n, Follow: follow, Host: host}
	if !lo.console {
		opts.Source, opts.Filter = "requests", lo.filter.Values()
	}
	for {
		start := time.Now()
		body, err := c.WorkloadLogs(ctx, tn.TenantID, pos[0], opts)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var cerr error
		if lo.console {
			_, cerr = io.Copy(out, body) // with --json: one log event per line (agentjson.go)
		} else {
			cerr = copyRecords(os.Stdout, body) // logs_records.go
		}
		body.Close()
		if !follow || ctx.Err() != nil {
			return nil
		}
		// The host ends a follow stream after a while, or the stream broke
		// (mgmt or the host restarted): follow again, new lines only.
		if cerr != nil || time.Since(start) < time.Second {
			time.Sleep(time.Second)
		}
		opts.N = 0
	}
}
