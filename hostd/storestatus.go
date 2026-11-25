package hostd

// Degraded platform stores (#147), host side: the platform's Store object
// (hostd/gen/js/store.js) reports through POST /v1/store/status when it
// stops its replay at a journal entry it can't apply (store_degraded), and
// when it is healthy again. hostd adds the tenant, the workload and its
// lease epoch, and passes the report to mgmt (ReportStoreStatus), which
// records it on the store and audits it.

import (
	"net/http"
)

func (h *Host) storeStatus(w http.ResponseWriter, r *http.Request, wl *workload, epoch int64, req storeReq) {
	if err := h.mgmt.ReportStoreStatus(r.Context(), wl.tenantID, wl.name, req.Kind, req.Name, epoch, req.Incarnation, req.Applied, req.Degraded); err != nil {
		writeStoreError(w, asAPIError(err))
		return
	}
	writeJSON(w, 200, map[string]any{})
}
