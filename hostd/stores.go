package hostd

// Platform stores (ADR-044), host side. The platform's Store
// Durable Object (hostd/gen/js/store.js) reaches mgmt's store journal only
// through hostd's local host API (its HOSTD binding, which tenant code
// can't reach):
//
//	POST /v1/store/read   {kind, name, after}                       → journal page
//	POST /v1/store/append {kind, name, after, incarnation, entries} → {incarnation, head}
//	POST /v1/store/status {kind, name, incarnation, applied, degraded?} → {} (storestatus.go)
//
// hostd adds what the platform can't vouch for itself: the tenant, the
// workload and the lease epoch this host holds, so mgmt fences every read
// and write to the current lease holder. Only the stores the running
// deployment declares (bundle.App.Stores) are reachable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// storeBodyLimit bounds a store API body (an append: base64 data + JSON).
const storeBodyLimit = 6 << 20

// storeReadTimeout bounds one journal read (a catch-up page, at most
// mgmt.StoreReadMaxBytes) against a slow or black-holed mgmt. The store
// object stops waiting for a catch-up long before this and serves its
// local copy (READ_WAIT_MS in gen/js/store.js); this bounds how long a
// stuck catch-up holds the object's queue, where the writes and syncs
// behind it wait. 10 s leaves room for a full page over a slow link (a
// store rebuilding on a new host reads page after page). Appends keep
// the request's own context and the mgmt client's 30 s timeout: a write
// is acknowledged only once mgmt has it (RPO 0).
const storeReadTimeout = 10 * time.Second

type storeReq struct {
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	After       int64                  `json:"after"`
	Incarnation string                 `json:"incarnation"`
	Entries     []mgmtapi.StoreEntryIn `json:"entries"`

	// status (storestatus.go)
	Applied  int64                  `json:"applied"`
	Degraded *mgmtapi.StoreDegraded `json:"degraded"`
}

var storeErrStatus = map[string]int{
	"bad_request":             400,
	"store_limit":             400,
	"storage_quota_exceeded":  429,
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
		rctx, cancel := context.WithTimeout(ctx, storeReadTimeout)
		p, err := h.mgmt.ReadStoreJournal(rctx, wl.tenantID, wl.name, req.Kind, req.Name, epoch, req.After, 0)
		cancel()
		if err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			writeStoreError(w, &apiError{Code: "store_unavailable", Detail: fmt.Sprintf("mgmt did not answer a store journal read within %s", storeReadTimeout)})
			return
		}
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
	case "status":
		h.storeStatus(w, r, wl, epoch, req)
	default:
		writeStoreError(w, &apiError{Code: "bad_request", Detail: "unknown store op"})
	}
}
