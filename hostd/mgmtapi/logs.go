package mgmtapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// LogsOptions selects a workload log read (GetWorkloadLogs).
type LogsOptions struct {
	N      int
	Follow bool
	Host   string // only the replica on this host ("" = all)
	// Source "requests" reads the structured request records (JSON Lines)
	// with Filter (obs.Filter.Values); "" is the raw console.
	Source string
	Filter url.Values
}

// WorkloadLogs is GET /v1/tenants/{id}/workloads/{workload}/logs: the
// body streams the log as text (close it). The request has no overall
// timeout (follow streams); bound it with ctx. Non-2xx is *Error.
func (c *Client) WorkloadLogs(ctx context.Context, tenantID, workload string, o LogsOptions) (io.ReadCloser, error) {
	var tr http.RoundTripper
	if c.HTTP != nil {
		tr = c.HTTP.Transport
	}
	vc, err := v1.NewClient(c.Base, v1.WithHTTPClient(&http.Client{Transport: tr}), v1.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		c.setHeaders(req)
		return nil
	}))
	if err != nil {
		return nil, err
	}
	n, follow := o.N, o.Follow
	p := &v1.GetWorkloadLogsParams{N: &n, Follow: &follow}
	if o.Host != "" {
		p.Host = &o.Host
	}
	if o.Source != "" {
		src := v1.GetWorkloadLogsParamsSource(o.Source)
		p.Source = &src
		logsFilterParams(p, o.Filter)
	}
	resp, err := vc.GetWorkloadLogs(ctx, tenantID, workload, p)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		e := &Error{Status: resp.StatusCode}
		if json.Unmarshal(b, e) != nil || e.Code == "" {
			e.Code, e.Message = "http_"+fmt.Sprint(resp.StatusCode), strings.TrimSpace(string(b))
		}
		return nil, e
	}
	return resp.Body, nil
}
