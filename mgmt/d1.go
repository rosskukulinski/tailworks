package mgmt

// D1 operator routes (ADR-038): `tw d1 execute`, `tw d1 migrations
// apply` and `tw d1 export`. A D1 database is the workload's platform
// store of kind "d1" (create, list and delete are the generic store
// routes); its data is served by the store object in the workload's
// workerd, so these routes go to the host holding the workload's lease
// (hostd's POST /v1/store-admin, over the same mgmt → hostd channel as
// logs), which hands them to its platform worker. Writes there are
// journal-first like every write through the binding.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

const (
	// d1BodyLimit bounds a QueryD1 body (a migration).
	d1BodyLimit = 8 << 20
	// d1RespLimit bounds what a host answers (results, an export).
	d1RespLimit = 64 << 20
)

// d1Host is the host holding tenantID's workload lease.
func (m *Mgmt) d1Host(ctx context.Context, tenantID, workload, name string) (string, error) {
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return "", lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if !validName(workload) {
		return "", errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if !ValidStoreName(name) {
		return "", errf(400, "bad_request", "bad database name %q", name)
	}
	l, err := m.store.GetLease(ctx, LeaseName(tenantID, workload))
	if errors.Is(err, ErrNotFound) || err == nil && l.Holder == "" {
		return "", errf(404, "unknown_workload", "tenant %s has no running workload %q: a D1 database is reached through the deployment that binds it", tenantID, workload)
	}
	return l.Holder, err
}

// hostCall sends one request to host's push API (its tsnet node through
// mgmt's, the local-dev unix socket, or an in-process host) and returns
// the answer's status and body.
func (m *Mgmt) hostCall(ctx context.Context, host, method, pathQuery string, body []byte, limit int64) (int, []byte, error) {
	do := func(target string) (*http.Response, error) {
		base := "http://hostd"
		var tr http.RoundTripper
		switch {
		case target == EmbeddedPushURL:
			m.mu.Lock()
			tr = handlerTransport{m.inprocPush[host]}
			m.mu.Unlock()
		case strings.HasPrefix(target, "unix://"):
			sock := strings.TrimPrefix(target, "unix://")
			tr = &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}}
		default:
			dial := m.cfg.TailnetDial
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
			base, tr = target, &http.Transport{DisableKeepAlives: true, DialContext: dial}
		}
		req, err := http.NewRequestWithContext(ctx, method, base+pathQuery, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		return (&http.Client{Transport: tr}).Do(req)
	}
	u := m.pushTarget(host)
	if u == "" {
		return 0, nil, errf(502, "host_unavailable", "host %s has no push URL", host)
	}
	resp, err := do(u)
	if err != nil && ctx.Err() == nil {
		// A stale cached target (the host re-registered): re-read, retry once.
		m.mu.Lock()
		delete(m.pushURLs, host)
		m.mu.Unlock()
		if u2 := m.pushTarget(host); u2 != "" && u2 != u {
			resp, err = do(u2)
		}
	}
	if err != nil {
		return 0, nil, errf(502, "host_unavailable", "host %s did not answer: %v", host, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, errf(502, "host_unavailable", "host %s: %v", host, err)
	}
	if int64(len(b)) > limit {
		return 0, nil, errf(400, "store_limit", "the answer is over %d bytes", limit)
	}
	return resp.StatusCode, b, nil
}

// d1Admin runs a store admin action on the database and returns the
// platform's JSON answer.
func (m *Mgmt) d1Admin(ctx context.Context, tenantID, workload, name, action string, arg []byte) ([]byte, string, error) {
	host, err := m.d1Host(ctx, tenantID, workload, name)
	if err != nil {
		return nil, "", err
	}
	q := url.Values{"tenant": {tenantID}, "workload": {workload}, "kind": {"d1"}, "name": {name}, "action": {action}}
	st, b, err := m.hostCall(ctx, host, "POST", "/v1/store-admin?"+q.Encode(), arg, d1RespLimit)
	if err != nil {
		return nil, host, err
	}
	if st != 200 {
		var he struct{ Code, Message string }
		json.Unmarshal(b, &he)
		switch he.Code {
		case "unknown_workload":
			return nil, host, errf(404, "unknown_workload", "workload %s is not running on host %s (it may be moving; retry)", workload, host)
		case "unknown_store":
			return nil, host, errf(404, "unknown_store", "%s", he.Message)
		case "store_limit", "bad_request":
			return nil, host, errf(400, he.Code, "%s", he.Message)
		case "lease_lost":
			return nil, host, errf(409, "lease_lost", "%s", he.Message)
		case "store_degraded":
			return nil, host, errf(409, "store_degraded", "%s", he.Message)
		}
		return nil, host, errf(503, "store_unavailable", "host %s: %s %s", host, he.Code, he.Message)
	}
	return b, host, nil
}

func (a *v1api) QueryD1(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName) {
	ctx := r.Context()
	raw, err := io.ReadAll(io.LimitReader(r.Body, d1BodyLimit+1))
	if err != nil || len(raw) > d1BodyLimit {
		writeErr(w, r, errf(400, "bad_request", "the body is over %d bytes", d1BodyLimit))
		return
	}
	var q v1.D1Query
	if err := json.Unmarshal(raw, &q); err != nil || len(q.Queries) == 0 {
		writeErr(w, r, errf(400, "bad_request", "want {\"queries\": [{\"sql\": …, \"params\": […]}, …]}"))
		return
	}
	b, host, err := a.s.M.d1Admin(ctx, id, workload, name, "query", raw)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var rs []v1.D1Result
	if err := json.Unmarshal(b, &rs); err != nil {
		var one v1.D1Result
		if json.Unmarshal(b, &one) == nil && !one.Success {
			writeErr(w, r, errf(400, "d1_error", "%s", str(one.Error)))
			return
		}
		writeErr(w, r, fmt.Errorf("d1: host %s answered %d bytes that are not results", host, len(b)))
		return
	}
	changed := false
	for _, x := range rs {
		if x.Meta != nil {
			if c, _ := (*x.Meta)["changed_db"].(bool); c {
				changed = true
			}
		}
	}
	by := principalOf(ctx).Actor(r)
	if changed {
		a.s.M.audit(ctx, AuditRecord{TenantID: id, Kind: "d1_write", ActingFor: by},
			map[string]any{"workload": workload, "database": name, "statements": len(q.Queries), "host": host, "by": by})
	} else {
		a.s.M.log.Info("d1 read", "tenant", id, "workload", workload, "database", name, "statements", len(q.Queries), "by", by)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, rs)
}

func (a *v1api) ExportD1(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName) {
	ctx := r.Context()
	b, _, err := a.s.M.d1Admin(ctx, id, workload, name, "export", []byte(`{}`))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var ex struct {
		SQL   string `json:"sql"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &ex); err != nil {
		writeErr(w, r, err)
		return
	}
	if ex.Error != "" {
		writeErr(w, r, errf(400, "store_limit", "%s", ex.Error))
		return
	}
	a.s.M.log.Info("d1 export", "tenant", id, "workload", workload, "database", name, "bytes", len(ex.SQL), "by", principalOf(ctx).Actor(r))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	io.WriteString(w, ex.SQL)
}
