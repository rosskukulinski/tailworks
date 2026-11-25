package mgmtapi

import (
	"context"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// ServiceBindingTarget is where a service binding's calls go
// (ADR-service-bindings).
type ServiceBindingTarget struct {
	Binding    string `json:"binding"`
	Service    string `json:"service"`
	Entrypoint string `json:"entrypoint,omitempty"`
	ObjectID   string `json:"objectID"`
	Hostname   string `json:"hostname"`
	MagicDNS   string `json:"magicDNS"`
	Status     string `json:"status"`
}

// ServiceBindingTarget is GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}?epoch=N:
// the target entry node of workload's binding, for the lease holder at epoch.
func (c *Client) ServiceBindingTarget(ctx context.Context, tenantID, workload, binding string, epoch int64) (ServiceBindingTarget, error) {
	var out ServiceBindingTarget
	resp, err := c.V1().GetServiceBindingTargetWithResponse(ctx, tenantID, workload, binding, &v1.GetServiceBindingTargetParams{Epoch: epoch})
	if err != nil {
		return out, err
	}
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}
