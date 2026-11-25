package mgmt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Workload logs for linked machines (`tw logs <workload>` over `tw
// link`): GET /v1/tenants/{id}/workloads/{workload}/logs. The log is a file
// on the host that runs the workload, so mgmt finds the lease holder of the
// workload (and of each replica) and reads hostd's GET /v1/logs over the
// same mgmt → hostd channel as kills and assignment pushes. hostd resolves
// the file from its own record of (tenant, workload); no path crosses the
// wire. Admin only, like every other tenant-scoped read; a read is not
// audited (only mutations are), but the actor is logged.

// MaxLogLines caps n (hostd.MaxLogLines).
const MaxLogLines = 10000

const defaultLogLines = 50

// logSource is one replica of a workload and the host holding its lease.
type logSource struct {
	workload, host string
}

// LogsQuery is a logs read.
type LogsQuery struct {
	N      int
	Follow bool
	Host   string // only the replica on this host ("" = all)
	// Source is "console" (default) or "requests" (ADR-045), and
	// Filter the record filters hostd applies (obs.Filter's parameters).
	Source string
	Filter url.Values
}

// logSources lists the running replicas of tenantID's workload (the
// workload itself first) with their lease holders.
func (m *Mgmt) logSources(ctx context.Context, tenantID, workload, host string) ([]logSource, error) {
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return nil, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if !validName(workload) {
		return nil, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	names := []string{workload}
	reps, err := m.replicasOf(ctx, tenantID, workload)
	if err != nil {
		return nil, err
	}
	for i := 1; i <= MaxReplicas; i++ {
		if d, ok := reps[i]; ok {
			names = append(names, d.Workload)
		}
	}
	var out []logSource
	for _, name := range names {
		l, err := m.store.GetLease(ctx, LeaseName(tenantID, name))
		if errors.Is(err, ErrNotFound) || err == nil && l.Holder == "" {
			continue
		}
		if err != nil {
			return nil, err
		}
		if host != "" && l.Holder != host {
			continue
		}
		out = append(out, logSource{workload: name, host: l.Holder})
	}
	if len(out) == 0 {
		if host != "" {
			return nil, errf(404, "unknown_workload", "workload %s of tenant %s has no running replica on host %s", workload, tenantID, host)
		}
		return nil, errf(404, "unknown_workload", "tenant %s has no running workload %q", tenantID, workload)
	}
	return out, nil
}

// hostLogs opens hostd's GET /v1/logs for one source. The request lives as
// long as ctx (no client timeout: follow streams).
func (m *Mgmt) hostLogs(ctx context.Context, tenantID string, src logSource, q LogsQuery) (*http.Response, error) {
	v := url.Values{"tenant": {tenantID}, "workload": {src.workload}, "n": {strconv.Itoa(q.N)}}
	if q.Source != "" {
		v.Set("source", q.Source)
	}
	for k, vs := range q.Filter {
		v[k] = vs
	}
	if q.Follow {
		v.Set("follow", "1")
	}
	open := func(target string) (*http.Response, error) {
		base := "http://hostd"
		var dial func(ctx context.Context, network, addr string) (net.Conn, error)
		if sock, ok := strings.CutPrefix(target, "unix://"); ok {
			dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}
		} else {
			base, dial = target, m.cfg.TailnetDial // the host's node, through mgmt's
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
		}
		var tr http.RoundTripper = &http.Transport{DialContext: dial, DisableKeepAlives: true}
		if target == EmbeddedPushURL {
			// A host agent in this process (single-process tw up): its push
			// handler, no dial (logs_inproc.go).
			m.mu.Lock()
			tr, base = handlerTransport{m.inprocPush[src.host]}, "http://hostd"
			m.mu.Unlock()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/logs?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		return (&http.Client{Transport: tr}).Do(req)
	}
	u := m.pushTarget(src.host)
	if u == "" {
		return nil, errors.New("host has no push URL")
	}
	resp, err := open(u)
	if err != nil && ctx.Err() == nil {
		// A stale cached target (the host re-registered): re-read, retry once.
		m.mu.Lock()
		delete(m.pushURLs, src.host)
		m.mu.Unlock()
		if u2 := m.pushTarget(src.host); u2 != "" && u2 != u {
			resp, err = open(u2)
		}
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var he struct{ Code, Message string }
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&he)
		switch he.Code {
		case "unknown_workload":
			return nil, errf(404, "unknown_workload", "workload %s is not running on host %s (it may be moving; retry)", src.workload, src.host)
		case "no_logs":
			return nil, errf(404, "no_logs", "host %s has no log for workload %s yet", src.host, src.workload)
		}
		return nil, fmt.Errorf("hostd %s: %s %s", src.host, resp.Status, he.Message)
	}
	return resp, nil
}

// asHostUnavailable makes a transport failure a typed 502.
func asHostUnavailable(src logSource, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return errf(502, "host_unavailable", "host %s (holding workload %s) did not answer: %v", src.host, src.workload, err)
}

// streamLogs writes the sources' logs to w: one source as is, several
// with each line prefixed "[<host>] ".
func (m *Mgmt) streamLogs(ctx context.Context, w http.ResponseWriter, tenantID string, srcs []logSource, q LogsQuery) error {
	resps := make([]*http.Response, len(srcs))
	errs := make([]error, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resps[i], errs[i] = m.hostLogs(ctx, tenantID, src, q)
		}()
	}
	wg.Wait()
	defer func() {
		for _, r := range resps {
			if r != nil {
				r.Body.Close()
			}
		}
	}()
	ok := 0
	for _, r := range resps {
		if r != nil {
			ok++
		}
	}
	if ok == 0 {
		return asHostUnavailable(srcs[0], errs[0])
	}
	w.Header().Set("Content-Type", logsContentType(q.Source))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	out := &logWriter{w: w, rc: http.NewResponseController(w)}
	if len(srcs) == 1 {
		buf := make([]byte, 32<<10)
		for {
			k, err := resps[0].Body.Read(buf)
			if k > 0 {
				if out.write(buf[:k]) != nil {
					return nil
				}
			}
			if err != nil {
				return nil
			}
		}
	}
	for i, src := range srcs {
		if resps[i] == nil {
			out.write([]byte("[" + src.host + "] tw: " + asHostUnavailable(src, errs[i]).Error() + "\n"))
		}
	}
	copyOne := func(i int) {
		prefix := []byte("[" + srcs[i].host + "] ")
		rd := bufio.NewReaderSize(resps[i].Body, 64<<10)
		for {
			line, err := rd.ReadSlice('\n')
			if len(line) > 0 {
				b := append(append(make([]byte, 0, len(prefix)+len(line)+1), prefix...), line...)
				if line[len(line)-1] != '\n' {
					b = append(b, '\n') // a long line (or the last) is split here
				}
				if out.write(b) != nil {
					return
				}
			}
			if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
				return
			}
		}
	}
	if !q.Follow {
		for i := range srcs { // replica order, one after the other
			if resps[i] != nil {
				copyOne(i)
			}
		}
		return nil
	}
	for i := range srcs {
		if resps[i] != nil {
			wg.Add(1)
			go func() { defer wg.Done(); copyOne(i) }()
		}
	}
	wg.Wait()
	return nil
}

// logWriter serialises whole writes (lines, with several sources) and
// flushes each one.
type logWriter struct {
	mu sync.Mutex
	w  io.Writer
	rc *http.ResponseController
}

func (l *logWriter) write(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	l.rc.Flush()
	return nil
}

func (a *v1api) GetWorkloadLogs(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.GetWorkloadLogsParams) {
	q := LogsQuery{N: defaultLogLines, Follow: params.Follow != nil && *params.Follow, Host: str(params.Host)}
	if params.N != nil {
		q.N = *params.N
	}
	if q.N < 0 || q.N > MaxLogLines {
		writeErr(w, r, errf(400, "bad_request", "n must be 0..%d", MaxLogLines))
		return
	}
	if err := logsSource(&q, params); err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	srcs, err := a.s.M.logSources(ctx, id, workload, q.Host)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	hosts := make([]string, len(srcs))
	for i, s := range srcs {
		hosts[i] = s.host
	}
	a.s.M.log.Info("workload logs read", "tenant", id, "workload", workload, "hosts", hosts, "follow", q.Follow, "by", principalOf(ctx).Actor(r))
	if err := a.s.M.streamLogs(ctx, w, id, srcs, q); err != nil {
		writeErr(w, r, err)
	}
}
