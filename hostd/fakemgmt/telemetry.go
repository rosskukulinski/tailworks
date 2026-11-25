package fakemgmt

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// A tenant's OTLP export (ADR-045), in memory: tests set it
// directly; hosts read it with the lease-fenced grant, like secrets.

// SetTelemetry sets (nil: clears) tenantID's OTLP export.
func (s *Server) SetTelemetry(tenantID string, o *mgmtapi.OTLPExport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.telemetry == nil {
		s.telemetry = map[string]*mgmtapi.OTLPExport{}
	}
	s.telemetry[tenantID] = o
}

func (s *Server) telemetryGrant(w http.ResponseWriter, r *http.Request) {
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	name := secretKey(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[name]
	if l == nil || l.epoch != epoch {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, fmt.Sprintf("lease %s is not held at epoch %d", name, epoch))
		return
	}
	if hid := r.Header.Get("Tw-Host-ID"); hid != "" && hid != l.host {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, "host "+hid+" does not hold the lease")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, mgmtapi.TelemetryGrant{TenantID: r.PathValue("id"), Workload: r.PathValue("workload"), Epoch: epoch,
		OTLP: s.telemetry[r.PathValue("id")], GrantedAt: ms(time.Now())})
}
