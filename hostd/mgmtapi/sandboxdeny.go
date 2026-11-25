package mgmtapi

import "context"

// SandboxDenyPaths is GET /v1/hosts/{id}/sandbox-deny: every tenant's
// credential file paths (no tenant IDs), for the workload sandbox's deny
// list.
func (c *Client) SandboxDenyPaths(ctx context.Context, hostID string) ([]string, error) {
	resp, err := c.V1().GetHostSandboxDenyWithResponse(ctx, hostID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Paths []string `json:"paths"`
	}
	if err := result(resp.HTTPResponse, resp.Body, &out); err != nil {
		return nil, err
	}
	return out.Paths, nil
}
