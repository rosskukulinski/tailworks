package mgmtapi

import (
	"context"
	"fmt"
	"net/url"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// OTLPExport is a tenant's OTLP export (ADR-045), as a host's
// grant carries it. It never prints its header values.
type OTLPExport struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
	Signals  []string          `json:"signals"`
}

func (o OTLPExport) String() string {
	return fmt.Sprintf("OTLPExport{%s, %d headers [REDACTED], %v}", o.Endpoint, len(o.Headers), o.Signals)
}

// TelemetryGrant is the answer to POST
// /v1/tenants/{id}/workloads/{workload}/telemetry-grants.
type TelemetryGrant struct {
	TenantID  string      `json:"tenantID"`
	Workload  string      `json:"workload"`
	Epoch     int64       `json:"epoch"`
	OTLP      *OTLPExport `json:"otlp,omitempty"`
	GrantedAt int64       `json:"grantedAt"`
}

// TelemetryGrant is POST …/telemetry-grants?epoch=N (201): the tenant's
// OTLP export config (nil: none), released only to the workload's lease
// holder at epoch.
func (c *Client) TelemetryGrant(ctx context.Context, tenantID, workload string, epoch int64) (*OTLPExport, error) {
	resp, err := c.V1().CreateTelemetryGrantWithResponse(ctx, tenantID, workload, &v1.CreateTelemetryGrantParams{Epoch: epoch})
	if err != nil {
		return nil, err
	}
	var out TelemetryGrant
	if err := result(resp.HTTPResponse, resp.Body, &out); err != nil {
		return nil, err
	}
	return out.OTLP, nil
}

// TelemetryView is GET /v1/tenants/{id}/telemetry (header names only).
type TelemetryView struct {
	TenantID string `json:"tenantID"`
	OTLP     *struct {
		Endpoint    string   `json:"endpoint"`
		HeaderNames []string `json:"headerNames"`
		Signals     []string `json:"signals"`
	} `json:"otlp,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// Telemetry is GET /v1/tenants/{id}/telemetry.
func (c *Client) Telemetry(ctx context.Context, tenantID string) (TelemetryView, error) {
	var out TelemetryView
	resp, err := c.V1().GetTenantTelemetryWithResponse(ctx, tenantID)
	if err != nil {
		return out, err
	}
	return out, result(resp.HTTPResponse, resp.Body, &out)
}

// PutTelemetry is PUT /v1/tenants/{id}/telemetry.
func (c *Client) PutTelemetry(ctx context.Context, tenantID string, o OTLPExport) (TelemetryView, error) {
	var out TelemetryView
	body := v1.TelemetryPut{Otlp: v1.OTLPExportPut{Endpoint: o.Endpoint}}
	if len(o.Headers) > 0 {
		h := o.Headers
		body.Otlp.Headers = &h
	}
	if len(o.Signals) > 0 {
		ss := make([]v1.TelemetrySignal, len(o.Signals))
		for i, s := range o.Signals {
			ss[i] = v1.TelemetrySignal(s)
		}
		body.Otlp.Signals = &ss
	}
	resp, err := c.V1().PutTenantTelemetryWithResponse(ctx, tenantID, body)
	if err != nil {
		return out, err
	}
	return out, result(resp.HTTPResponse, resp.Body, &out)
}

// DeleteTelemetry is DELETE /v1/tenants/{id}/telemetry.
func (c *Client) DeleteTelemetry(ctx context.Context, tenantID string) error {
	resp, err := c.V1().DeleteTenantTelemetryWithResponse(ctx, tenantID)
	if err != nil {
		return err
	}
	return result(resp.HTTPResponse, resp.Body, nil)
}

// logsFilterParams copies record filters (obs.Filter.Values) into p.
func logsFilterParams(p *v1.GetWorkloadLogsParams, f url.Values) {
	str := func(k string) *string {
		if v := f.Get(k); v != "" {
			return &v
		}
		return nil
	}
	p.Object, p.Status, p.Outcome, p.Caller, p.Trace = str("object"), str("status"), str("outcome"), str("caller"), str("trace")
	if f.Get("errors") == "1" {
		t := true
		p.Errors = &t
	}
}
