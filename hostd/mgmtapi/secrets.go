package mgmtapi

import (
	"context"
	"fmt"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// SecretGrant is the answer to POST
// /v1/tenants/{id}/workloads/{workload}/secret-grants. It never prints its
// values.
type SecretGrant struct {
	TenantID  string            `json:"tenantID"`
	Workload  string            `json:"workload"`
	Epoch     int64             `json:"epoch"`
	Secrets   map[string]string `json:"secrets"`
	GrantedAt int64             `json:"grantedAt"`
}

func (g SecretGrant) String() string {
	return fmt.Sprintf("SecretGrant{%s/%s epoch %d, %d secrets [REDACTED]}", g.TenantID, g.Workload, g.Epoch, len(g.Secrets))
}

// SecretMeta is one workload secret without its value (GET …/secrets).
type SecretMeta struct {
	TenantID  string `json:"tenantID"`
	Workload  string `json:"workload"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// WorkloadSecrets is POST /v1/tenants/{id}/workloads/{workload}/secret-grants?epoch=N
// (201): the workload's secret values, released only to the lease holder
// at epoch. An empty map means the workload has none.
func (c *Client) WorkloadSecrets(ctx context.Context, tenantID, workload string, epoch int64) (map[string]string, error) {
	resp, err := c.V1().CreateWorkloadSecretGrantWithResponse(ctx, tenantID, workload, &v1.CreateWorkloadSecretGrantParams{Epoch: epoch})
	if err != nil {
		return nil, err
	}
	var out SecretGrant
	if err := result(resp.HTTPResponse, resp.Body, &out); err != nil {
		return nil, err
	}
	return out.Secrets, nil
}
