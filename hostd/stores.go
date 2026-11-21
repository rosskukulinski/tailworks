package hostd

// Platform stores (ADR-044), host side. The platform's Store
// Durable Object (hostd/gen/js/store.js) reaches mgmt's store journal only
// through hostd's local host API (its HOSTD binding, which tenant code
// can't reach):
//
//	POST /v1/store/read   {kind, name, after}                       → journal page
//	POST /v1/store/append {kind, name, after, incarnation, entries} → {incarnation, head}
//
// hostd adds what the platform can't vouch for itself: the tenant, the
// workload and the lease epoch this host holds, so mgmt fences every read
// and write to the current lease holder. Only the stores the running
// deployment declares (bundle.App.Stores) are reachable.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// storeBodyLimit bounds a store API body (an append: base64 data + JSON).
const storeBodyLimit = 6 << 20

type storeReq struct {
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	After       int64                  `json:"after"`
	Incarnation string                 `json:"incarnation"`
	Entries     []mgmtapi.StoreEntryIn `json:"entries"`
}

var storeErrStatus = map[string]int{
	"bad_request":             400,
	"store_limit":             400,
	mgmtapi.CodeStoreConflict: 409,
	mgmtapi.CodeLeaseLost:     409,
	"store_degraded":          409,
	"store_unavailable":       503,
	"identity_pending":        503,
	"hostd_unavailable":       503,
	"unknown_store":           404,
}

func writeStoreError(w http.ResponseWriter, e *apiError) {
	st := storeErrStatus[e.Code]
	if st == 0 {
		st = 503 // mgmt or the network: the store object serves what it has and retries
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(st)
	json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "message": e.Detail})
}

// storeAPI serves /v1/store/* for inst's platform.
func (h *Host) storeAPI(inst *instance, w http.ResponseWriter, r *http.Request) {
	var req storeReq
	if err := json.NewDecoder(io.LimitReader(r.Body, storeBodyLimit)).Decode(&req); err != nil {
		writeStoreError(w, &apiError{Code: "bad_request", Detail: "store: " + err.Error()})
		return
	}
	declared := false
	for _, b := range inst.app.Stores {
		if b.Kind == req.Kind && b.Name == req.Name {
			declared = true
			break
		}
	}
	if !declared {
		writeStoreError(w, &apiError{Code: "bad_request", Detail: "store " + req.Kind + "/" + req.Name + " is not bound by this deployment"})
		return
	}
	wl := inst.w
	epoch := wl.epochNow()
	ctx := r.Context()
	switch strings.TrimPrefix(r.URL.Path, "/v1/store/") {
	case "read":
		p, err := h.mgmt.ReadStoreJournal(ctx, wl.tenantID, wl.name, req.Kind, req.Name, epoch, req.After, 0)
		if err != nil {
			writeStoreError(w, asAPIError(err))
			return
		}
		writeJSON(w, 200, p)
	case "append":
		res, err := h.mgmt.AppendStoreJournal(ctx, wl.tenantID, wl.name, req.Kind, req.Name, epoch, req.After, req.Incarnation, req.Entries)
		if err != nil {
			writeStoreError(w, asAPIError(err))
			return
		}
		writeJSON(w, 200, res)
	default:
		writeStoreError(w, &apiError{Code: "bad_request", Detail: "unknown store op"})
	}
}
