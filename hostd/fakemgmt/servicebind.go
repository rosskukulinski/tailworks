package fakemgmt

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/svcbind"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// serviceBinding is GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}
// (mgmt.ServiceBindingTarget's contract, ADR-service-bindings).
func (s *Server) serviceBinding(w http.ResponseWriter, r *http.Request) {
	tn, wl, name := r.PathValue("id"), r.PathValue("workload"), r.PathValue("binding")
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[mgmtapi.LeaseName(tn, wl)]
	if l == nil || l.epoch != epoch {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, fmt.Sprintf("lease %s/%s is not held at epoch %d", tn, wl, epoch))
		return
	}
	if hid := r.Header.Get("Tw-Host-ID"); hid != "" && hid != l.host {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, "host "+hid+" does not hold the lease")
		return
	}
	latest := func(workload string) *deployment {
		var d *deployment
		for _, x := range s.sortedDeployments() {
			if x.tenantID == tn && x.workload == workload {
				d = x
			}
		}
		return d
	}
	var decl []svcbind.Binding
	if d := latest(wl); d != nil {
		bs, err := mgmt.BundleServices(d.bundle)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		decl = bs
	}
	b := svcbind.Find(decl, name)
	if b == nil {
		writeErr(w, 404, "service_not_found", "workload "+wl+" declares no service binding "+name)
		return
	}
	t := mgmt.ServiceBindingTarget{Binding: b.Binding, Service: b.Service, Entrypoint: b.Entrypoint}
	var entry *object
	for _, o := range s.objects {
		if o.TenantID == tn && o.workload == b.Service && o.Namespace == mgmtapi.WorkloadNamespace && o.ObjectName == b.Service {
			if entry == nil || o.created.After(entry.created) {
				entry = o
			}
		}
	}
	if entry == nil {
		if latest(b.Service) != nil {
			writeErr(w, 503, "identity_pending", "service "+b.Service+" has no entry node yet")
			return
		}
		writeErr(w, 404, "service_not_found", "no workload "+b.Service+" is deployed in this tenant")
		return
	}
	t.ObjectID, t.Hostname, t.Status, t.MagicDNS = entry.ObjectID, entry.Hostname, entry.Status, strings.TrimSuffix(entry.MagicDNS, ".")
	switch {
	case entry.Status == "killed":
		writeErr(w, 410, "killed", "service "+b.Service+"'s entry node was killed")
	case entry.Status == "failed":
		writeErr(w, 404, "service_not_found", "service "+b.Service+"'s entry node failed")
	case t.MagicDNS == "":
		writeErr(w, 503, "identity_pending", "service "+b.Service+"'s entry node has no MagicDNS name yet")
	default:
		writeJSON(w, 200, t)
	}
}
