package mgmtapi

import (
	"context"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// StoreDegraded is the journal entry a store object could not apply
// (#147): it stopped its replay before it (store_degraded).
type StoreDegraded struct {
	Seq   int64  `json:"seq"`
	Error string `json:"error"`
	Host  string `json:"host,omitempty"`
	At    int64  `json:"at"`
}

// ReportStoreStatus tells mgmt that a store object of incarnation inc,
// having applied the journal up to applied, is degraded at d (or healthy
// again: d nil), as the lease holder at epoch.
func (c *Client) ReportStoreStatus(ctx context.Context, tenantID, workload, kind, name string, epoch int64, inc string, applied int64, d *StoreDegraded) error {
	body := v1.StoreStatusReport{Incarnation: inc, Applied: applied}
	if d != nil {
		body.Degraded = &v1.StoreDegraded{Seq: d.Seq, Error: d.Error, At: d.At}
	}
	resp, err := c.V1().ReportStoreStatusWithResponse(ctx, tenantID, workload, kind, name, &v1.ReportStoreStatusParams{Epoch: &epoch}, body)
	if err != nil {
		return err
	}
	return result(resp.HTTPResponse, resp.Body, nil)
}
