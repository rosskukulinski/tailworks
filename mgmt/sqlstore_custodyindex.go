package mgmt

import (
	"context"
	"encoding/json"
)

// custodyIndexStore is what the custody backfill and migration read
// (custodymigrate.go). Implemented by SQLStore.
type custodyIndexStore interface {
	tenantPurgeStore
	secretChooser
	// CustodyTenantIDs lists every tenant ID the store has rows for: the
	// tenants, and the owners of rows a tenant delete from before tenant
	// purge (#107) left behind.
	CustodyTenantIDs(ctx context.Context) ([]string, error)
	// IndexedCustodyNames lists every name in custody_names.
	IndexedCustodyNames(ctx context.Context) ([]string, error)
	// SecretWorkloads lists, by tenant, the workloads the audit chain
	// records a secret change for: a workload that was never deployed has
	// secrets but no row that names it.
	SecretWorkloads(ctx context.Context) (map[string][]string, error)
}

func (s *SQLStore) CustodyTenantIDs(ctx context.Context) ([]string, error) {
	return s.column(ctx, s.db, `SELECT id FROM tenants
		UNION SELECT tenant_id FROM deployments
		UNION SELECT tenant_id FROM objects
		UNION SELECT tenant_id FROM stores
		UNION SELECT tenant_id FROM queues
		UNION SELECT tenant_id FROM warm_keys
		UNION SELECT tenant_id FROM workload_public
		UNION SELECT tenant_id FROM container_reservations
		UNION SELECT tenant_id FROM custody_names
		ORDER BY 1`)
}

func (s *SQLStore) IndexedCustodyNames(ctx context.Context) ([]string, error) {
	return s.column(ctx, s.db, "SELECT name FROM custody_names ORDER BY name")
}

func (s *SQLStore) SecretWorkloads(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q("SELECT tenant_id, raw FROM audit WHERE kind IN ('secret_put', 'secret_delete')"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[[2]string]bool{}
	out := map[string][]string{}
	for rows.Next() {
		var tenant, raw string
		if err := rows.Scan(&tenant, &raw); err != nil {
			return nil, err
		}
		var rec struct {
			Detail struct {
				Workload string `json:"workload"`
			} `json:"detail"`
		}
		if json.Unmarshal([]byte(raw), &rec) != nil || tenant == "" || rec.Detail.Workload == "" {
			continue
		}
		k := [2]string{tenant, rec.Detail.Workload}
		if !seen[k] {
			seen[k] = true
			out[tenant] = append(out[tenant], rec.Detail.Workload)
		}
	}
	return out, rows.Err()
}
