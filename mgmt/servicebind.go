package mgmt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/rosskukulinski/tailworks/internal/svcbind"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
	"github.com/tailscale/hujson"
)

// Service bindings (ADR-service-bindings): a host resolves a workload's
// wrangler "services" entry to the target workload's entry node in the same
// tenant, and sends the call there as the calling node.

// ServiceBindingTarget is GET .../service-bindings/{binding}.
type ServiceBindingTarget struct {
	Binding    string `json:"binding"`
	Service    string `json:"service"`
	Entrypoint string `json:"entrypoint,omitempty"`
	ObjectID   string `json:"objectID"`
	Hostname   string `json:"hostname"`
	MagicDNS   string `json:"magicDNS"`
	Status     string `json:"status"`
}

// BundleServices is a bundle's wrangler "services" (nil: none).
func BundleServices(bundle []byte) ([]svcbind.Binding, error) {
	files, err := bundleFiles(bundle, "wrangler.jsonc", "wrangler.json")
	if err != nil {
		return nil, err
	}
	for _, f := range []string{"wrangler.jsonc", "wrangler.json"} {
		b, ok := files[f]
		if !ok {
			continue
		}
		std, err := hujson.Standardize(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		bs, err := svcbind.FromWrangler(std)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		return bs, nil
	}
	return nil, nil
}

var serviceDecls sync.Map // deployment ID -> []svcbind.Binding

// workloadServices is the services config of a workload's latest
// deployment (nil when it has none or was never deployed).
func (m *Mgmt) workloadServices(ctx context.Context, tenantID, workload string) ([]svcbind.Binding, error) {
	d, err := m.latestDeployment(ctx, tenantID, workload)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c, ok := serviceDecls.Load(d.ID); ok {
		return c.([]svcbind.Binding), nil
	}
	if len(d.Bundle) == 0 {
		if d, err = m.store.GetDeployment(ctx, d.ID); err != nil {
			return nil, err
		}
	}
	bs, err := BundleServices(d.Bundle)
	if err != nil {
		return nil, err
	}
	serviceDecls.Store(d.ID, bs)
	return bs, nil
}

// ServiceBindingTarget resolves workload's binding for the host holding
// its lease at epoch (hostID "" is an admin, who need not give an epoch).
func (m *Mgmt) ServiceBindingTarget(ctx context.Context, tenantID, workload, binding, hostID string, epoch *int64) (ServiceBindingTarget, error) {
	var t ServiceBindingTarget
	if !validName(workload) {
		return t, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return t, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if err := m.fenceStore(ctx, tenantID, workload, hostID, epoch, "service/"+binding); err != nil {
		return t, err
	}
	decl, err := m.workloadServices(ctx, tenantID, workload)
	if err != nil {
		return t, err
	}
	b := svcbind.Find(decl, binding)
	if b == nil {
		return t, errf(404, "service_not_found", "workload %s declares no service binding %s (wrangler.jsonc services; redeploy to change)", workload, binding)
	}
	t = ServiceBindingTarget{Binding: b.Binding, Service: b.Service, Entrypoint: b.Entrypoint}
	objs, err := m.store.ListObjects(ctx, ObjectFilter{TenantID: tenantID, Workload: b.Service, Namespace: WorkloadNamespace})
	if err != nil {
		return t, err
	}
	var entry *Object
	for i := range objs {
		if objs[i].Name == b.Service {
			entry = &objs[i] // the newest wins
		}
	}
	if entry == nil {
		if _, err := m.latestDeployment(ctx, tenantID, b.Service); err == nil {
			return t, errf(503, "identity_pending", "service %s is deployed but its entry node does not exist yet; retry", b.Service)
		}
		return t, errf(404, "service_not_found", "binding %s: no workload %s is deployed in this tenant", binding, b.Service)
	}
	t.ObjectID, t.Hostname, t.Status = entry.ID, entry.Hostname, entry.Status
	t.MagicDNS = strings.TrimSuffix(entry.MagicDNS, ".")
	switch {
	case entry.Status == StatusKilled:
		return t, errf(410, "killed", "service %s's entry node %s was killed", b.Service, entry.ID)
	case !entry.Live():
		return t, errf(404, "service_not_found", "service %s's entry node %s is %s", b.Service, entry.ID, entry.Status)
	case t.MagicDNS == "":
		return t, errf(503, "identity_pending", "service %s's entry node %s has no MagicDNS name yet; retry", b.Service, entry.ID)
	}
	return t, nil
}

func (a *v1api) GetServiceBindingTarget(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, binding string, params v1.GetServiceBindingTargetParams) {
	ep := params.Epoch
	t, err := a.s.M.ServiceBindingTarget(r.Context(), id, workload, binding, hostOf(r), &ep)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}
