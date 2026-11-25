package mgmt

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/rosskukulinski/tailworks/internal/features"
)

// Host cordon and drain (ADR-041 §5). A cordoned host
// keeps what it runs, keeps heartbeating and keeps its leases; placement
// skips it (Candidate.Draining). Drain cordons, then re-places each
// workload assigned to the host elsewhere through the ordinary deploy path
// (`tw deploy --replace`): the cordoned host is ineligible, so placement
// moves the workload and its lease at the next epoch.

// CordonHost cordons (on) or uncordons a host. by is the actor.
func (m *Mgmt) CordonHost(ctx context.Context, id string, on bool, by string) (Host, error) {
	h, err := m.store.GetHost(ctx, id)
	if err != nil {
		return Host{}, lookupErr(err, errf(404, "not_found", "host %q not found", id))
	}
	cs, ok := m.store.(interface {
		SetHostCordon(ctx context.Context, id string, at int64, by string) error
	})
	if !ok {
		return Host{}, errf(501, "internal", "this store can't cordon hosts")
	}
	at, kind := int64(0), "host_uncordon"
	if on {
		if h.CordonedAt != 0 {
			return h, nil // idempotent: keep who cordoned it first
		}
		at, kind = m.now().UnixMilli(), "host_cordon"
	} else if h.CordonedAt == 0 {
		return h, nil
	}
	if err := cs.SetHostCordon(ctx, id, at, cordonBy(on, by)); err != nil {
		return Host{}, err
	}
	m.audit(ctx, AuditRecord{Kind: kind, ActingFor: by}, map[string]any{"host": id})
	return m.store.GetHost(ctx, id)
}

func cordonBy(on bool, by string) string {
	if !on {
		return ""
	}
	return by
}

// DrainOptions are POST /v1/hosts/{id}/drain's body.
type DrainOptions struct {
	AllowLocalStateLoss bool `json:"allowLocalStateLoss"`
	DryRun              bool `json:"dryRun"`
}

// DrainMove is one workload a drain moved (or would move).
type DrainMove struct {
	TenantID     string `json:"tenantID"`
	Workload     string `json:"workload"`
	To           string `json:"to"`
	DeploymentID string `json:"deploymentID,omitempty"`
}

// DrainRefusal is one workload a drain left on the host.
type DrainRefusal struct {
	TenantID string `json:"tenantID"`
	Workload string `json:"workload"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// DrainResult is POST /v1/hosts/{id}/drain's answer.
type DrainResult struct {
	HostID   string         `json:"hostID"`
	Cordoned bool           `json:"cordoned"`
	DryRun   bool           `json:"dryRun,omitempty"`
	Moved    []DrainMove    `json:"moved"`
	Refused  []DrainRefusal `json:"refused"`
}

// DrainHost cordons hostID and moves its workloads (see the file comment).
func (m *Mgmt) DrainHost(ctx context.Context, hostID string, o DrainOptions, by string) (DrainResult, error) {
	res := DrainResult{HostID: hostID, DryRun: o.DryRun, Moved: []DrainMove{}, Refused: []DrainRefusal{}}
	if _, err := m.store.GetHost(ctx, hostID); err != nil {
		return res, lookupErr(err, errf(404, "not_found", "host %q not found", hostID))
	}
	if !o.DryRun {
		if _, err := m.CordonHost(ctx, hostID, true, by); err != nil {
			return res, err
		}
		res.Cordoned = true
	}
	ds, err := m.store.Assignments(ctx, hostID)
	if err != nil {
		return res, err
	}
	// Replicas move as their group (a replica can't be deployed by name).
	type key struct{ tenant, workload string }
	seen := map[key]bool{}
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	for _, a := range ds {
		d, err := m.store.GetDeployment(ctx, a.ID)
		if err != nil {
			res.Refused = append(res.Refused, DrainRefusal{a.TenantID, a.Workload, "internal", err.Error()})
			continue
		}
		workload := d.Workload
		if g := replicaGroupOf(d); g != "" {
			workload = g
		}
		k := key{d.TenantID, workload}
		if seen[k] {
			continue
		}
		seen[k] = true
		stateful := d.Fence
		if len(d.Bundle) > 0 {
			stateful, _ = BundleStateful(d.Bundle)
		}
		if stateful && !o.AllowLocalStateLoss {
			res.Refused = append(res.Refused, DrainRefusal{d.TenantID, workload, "move_unconfirmed",
				"the workload has Durable Objects whose storage is on this host's disk; it stays (bounce the host in place), or drain with --allow-local-state-loss to move it with empty storage"})
			continue
		}
		if o.DryRun {
			to, err := m.dryPlace(ctx, d, hostID)
			if err != nil {
				res.Refused = append(res.Refused, refusalOf(d.TenantID, workload, err))
				continue
			}
			res.Moved = append(res.Moved, DrainMove{TenantID: d.TenantID, Workload: workload, To: to})
			continue
		}
		resp, err := m.Deploy(ctx, DeployRequest{TenantID: d.TenantID, Workload: workload, Bundle: d.Bundle,
			Namespaces: namespacesIfNoConfig(d), Replace: true, ForceTakeover: stateful && o.AllowLocalStateLoss, By: by})
		if err != nil {
			res.Refused = append(res.Refused, refusalOf(d.TenantID, workload, err))
			continue
		}
		if resp.Host == hostID {
			res.Refused = append(res.Refused, DrainRefusal{d.TenantID, workload, "no_host", "placement kept it on this host"})
			continue
		}
		res.Moved = append(res.Moved, DrainMove{TenantID: d.TenantID, Workload: workload, To: resp.Host, DeploymentID: resp.DeploymentID})
	}
	m.audit(ctx, AuditRecord{Kind: "host_drain", ActingFor: by}, map[string]any{"host": hostID, "dryRun": o.DryRun,
		"moved": len(res.Moved), "refused": len(res.Refused), "allowLocalStateLoss": o.AllowLocalStateLoss})
	return res, nil
}

// replicaGroupOf is the group workload of a replica deployment ("" = not a
// replica).
func replicaGroupOf(d Deployment) string {
	for _, c := range d.Namespaces {
		if c.Class == WorkloadNamespace && c.ReplicaOf != "" {
			return c.ReplicaOf
		}
	}
	return ""
}

// namespacesIfNoConfig: a deployment whose bundle carries its config is
// redeployed from the bundle alone; one deployed with explicit namespaces
// (no tailworks.jsonc) gets them back.
func namespacesIfNoConfig(d Deployment) []ClassConfig {
	if _, err := BundleConfig(d.Bundle); err == nil {
		return nil
	}
	out := slices.Clone(d.Namespaces)
	for i := range out {
		out[i].ReplicaOf, out[i].Replicas = "", 0
	}
	return out
}

func refusalOf(tenant, workload string, err error) DrainRefusal {
	var e *Error
	if errors.As(err, &e) {
		return DrainRefusal{tenant, workload, e.Code, e.Message}
	}
	return DrainRefusal{tenant, workload, "internal", err.Error()}
}

// dryPlace is where placement would put d with hostID excluded.
func (m *Mgmt) dryPlace(ctx context.Context, d Deployment, hostID string) (string, error) {
	t, err := m.store.GetTenant(ctx, d.TenantID)
	if err != nil {
		return "", err
	}
	cfg, _ := BundleConfig(d.Bundle)
	need := placementNeed{Features: features.RequiredByBundle(d.Bundle)}
	dec, _, err := m.place(ctx, t, placementInput{Workload: d.Workload, Replace: true, Config: cfg, Need: need,
		Exclude: map[string]bool{hostID: true}})
	if err != nil {
		return "", err
	}
	return dec.Host, nil
}
