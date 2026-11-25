package hostd

// Operator operations on platform stores (ADR-044, ADR-038):
// `tw d1 execute`, `tw d1 migrations apply`, `tw d1 export`. A store's
// data lives in its Store object in the workload's workerd, so mgmt asks
// the host holding the workload's lease (the push API, like logs):
//
//	POST /v1/store-admin?tenant=&workload=&kind=&name=&action=   body: the op's JSON argument
//
// and hostd hands the op to its platform worker's admin route (the
// "store" admin op; only hostd can reach it). The store must be one the
// running deployment declares; the platform checks again.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

const (
	// storeAdminBodyLimit bounds an op's argument (a migration, a batch).
	storeAdminBodyLimit = 8 << 20
	// storeAdminRespLimit bounds its answer (results, an export).
	storeAdminRespLimit = 64 << 20
	// storeAdminTimeout bounds one op (a long migration).
	storeAdminTimeout = 2 * time.Minute
)

var storeAdminActionRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// StoreAdmin runs action on store (kind, name) of tenantID's workload on
// this host and returns the op's JSON answer.
func (h *Host) StoreAdmin(ctx context.Context, tenantID, workload, kind, name, action string, arg []byte) ([]byte, error) {
	if !tenantIDRE.MatchString(tenantID) || !logWorkloadRe.MatchString(workload) || !storeAdminActionRE.MatchString(action) {
		return nil, &apiError{Code: "bad_request", Detail: "bad tenant, workload or action"}
	}
	h.mu.Lock()
	w := h.workloads[tenantID+"/"+workload]
	h.mu.Unlock()
	var inst *instance
	if w != nil && w.tenantID == tenantID && w.name == workload {
		inst = w.current()
	}
	if inst == nil || inst.direct == nil || inst.app == nil {
		return nil, &apiError{Code: "unknown_workload", Detail: "workload " + workload + " of tenant " + tenantID + " is not running on host " + h.cfg.HostID}
	}
	declared := false
	for _, b := range inst.app.Stores {
		declared = declared || b.Kind == kind && b.Name == name
	}
	if !declared {
		return nil, &apiError{Code: "unknown_store", Detail: fmt.Sprintf("the running deployment of %s binds no %s store %q", workload, kind, name)}
	}
	blob, _ := json.Marshal(map[string]any{"tw": map[string]any{"kind": "admin", "op": "store", "storeKind": kind, "name": name, "action": action}})
	ctx, cancel := context.WithTimeout(ctx, storeAdminTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://platform/_tw/admin", bytes.NewReader(arg))
	if err != nil {
		return nil, err
	}
	req.Header.Set(netd.HeaderCF, string(blob))
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Transport: inst.direct.Transport}).Do(req)
	if err != nil {
		return nil, &apiError{Code: "store_unavailable", Detail: "the workload's platform did not answer: " + err.Error()}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, storeAdminRespLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > storeAdminRespLimit {
		return nil, &apiError{Code: "store_limit", Detail: fmt.Sprintf("the answer is over %d bytes", storeAdminRespLimit)}
	}
	if resp.StatusCode != 200 {
		var e struct{ Code, Message string }
		if json.Unmarshal(b, &e) != nil || e.Code == "" {
			e.Code, e.Message = "store_unavailable", fmt.Sprintf("platform answered %d", resp.StatusCode)
		}
		return nil, &apiError{Code: e.Code, Detail: e.Message}
	}
	return b, nil
}

// storeAdminHandler is the push route.
func (h *Host) storeAdminHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	arg, err := io.ReadAll(io.LimitReader(r.Body, storeAdminBodyLimit+1))
	if err == nil && len(arg) > storeAdminBodyLimit {
		err = errors.New("body over the limit")
	}
	if err != nil {
		writeStoreError(w, &apiError{Code: "store_limit", Detail: "store admin: " + err.Error()})
		return
	}
	b, err := h.StoreAdmin(r.Context(), q.Get("tenant"), q.Get("workload"), q.Get("kind"), q.Get("name"), q.Get("action"), arg)
	if err != nil {
		e := asAPIError(err)
		if e.Code == "unknown_workload" {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "message": e.Detail})
			return
		}
		writeStoreError(w, e)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.Write(b)
}
