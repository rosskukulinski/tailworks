package mgmt

// Degraded platform stores (#147). A store object that can't apply a
// journal entry stops its replay there (#106, ADR-044 §3): it serves
// reads from what it applied and refuses writes (store_degraded). Its
// host reports that here (POST …/stores/{kind}/{name}/status), once when
// it stops and once when it heals, so mgmt can show it (StoreInfo.Degraded:
// `tw d1 ls`, `tw kv namespace ls`, `tw r2 bucket ls`, `tw status`, the
// console) and audit it (store_replay_failed). An operator who decides the
// entry is poison skips it (POST …/repair, `tw d1 repair --skip <seq>`):
// mgmt deletes that one entry from the journal, so every host's replay
// goes on to the next, and audits it (store_repair).

import (
	"context"
	"errors"
	"net/http"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// StoreDegraded is the journal entry a store's object could not apply, as
// its host reported it.
type StoreDegraded struct {
	Seq   int64  `json:"seq"`
	Error string `json:"error"`
	Host  string `json:"host,omitempty"`
	At    int64  `json:"at"`
	// Workload is the workload (or replica, "<workload>--r<n>") whose
	// lease the reporting host held.
	Workload string `json:"workload,omitempty"`
}

// storeDegradedErrMax bounds the error text mgmt keeps (and audits).
const storeDegradedErrMax = 1000

// degradedStoresMax bounds ListDegradedStores.
const degradedStoresMax = 1000

func (m *Mgmt) storeDegradedJournal() (storeDegradedJournal, error) {
	sj, ok := m.store.(storeDegradedJournal)
	if !ok {
		return nil, errf(501, "internal", "this mgmt store has no platform stores")
	}
	return sj, nil
}

// ReportStoreStatus records what a host's store object reports: degraded
// (d, at entry d.Seq) or healthy (d nil, having applied up to applied). A
// host must hold the workload's lease at epoch. A report for an
// incarnation the store no longer has (or a store that is gone) is
// ignored. A healthy report clears the record if it comes from the host
// (and workload) that made it, or if it has applied the recorded entry and
// the recording host no longer runs that workload (it moved). A replica
// that applied the entry fine doesn't clear another replica's record.
func (m *Mgmt) ReportStoreStatus(ctx context.Context, tenantID, workload, kind, name, hostID string, epoch *int64, inc string, applied int64, d *StoreDegraded) error {
	sj, err := m.storeDegradedJournal()
	if err != nil {
		return err
	}
	scope, err := m.storeScope(ctx, tenantID, workload, kind, name)
	if err != nil {
		return err
	}
	what := kind + "/" + name
	if err := m.fenceStore(ctx, tenantID, workload, hostID, epoch, what); err != nil {
		return err
	}
	if inc == "" {
		return errf(400, "bad_request", "incarnation is required")
	}
	if d != nil {
		if d.Seq < 1 {
			return errf(400, "bad_request", "degraded.seq must be ≥ 1")
		}
		r := *d
		if len(r.Error) > storeDegradedErrMax {
			r.Error = r.Error[:storeDegradedErrMax] + "…"
		}
		r.Host, r.Workload = hostID, workload
		if r.At <= 0 {
			r.At = m.now().UnixMilli()
		}
		d = &r
	}
	// moved: the record is another host's (or workload's), and that host
	// no longer holds that workload's lease. Looked up before the update's
	// transaction; the update clears only the record it was looked up for.
	var moved *StoreDegraded
	if d == nil {
		if st, err := m.GetStore(ctx, tenantID, workload, kind, name); err == nil && st.Degraded != nil {
			rec := st.Degraded
			if (rec.Host != hostID || rec.Workload != workload) && applied >= rec.Seq {
				l, err := m.store.GetLease(ctx, LeaseName(tenantID, rec.Workload))
				if errors.Is(err, ErrNotFound) || err == nil && (l.Holder != rec.Host || l.ExpiresAt < m.now().UnixMilli()) {
					moved = rec
				}
			}
		}
	}
	audit, healed := false, false
	prev, err := sj.UpdateStoreDegraded(ctx, tenantID, scope, kind, name, inc, func(cur *StoreDegraded) (*StoreDegraded, bool) {
		audit, healed = false, false
		if d != nil {
			if cur != nil && *cur == *d {
				return nil, false
			}
			// The first report of an entry from a host; a repeat (a new
			// error text, the same entry) only updates the record.
			audit = cur == nil || cur.Seq != d.Seq || cur.Host != d.Host || cur.Workload != d.Workload
			return d, true
		}
		switch {
		case cur == nil:
			return nil, false
		case cur.Host == hostID && cur.Workload == workload, moved != nil && *cur == *moved:
			healed = true
			return nil, true
		}
		return nil, false
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case audit:
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "store_replay_failed"},
			map[string]any{"workload": scope, "kind": kind, "name": name, "incarnation": inc, "seq": d.Seq, "error": d.Error, "host": hostID, "reporter": workload})
		m.log.Warn("platform store degraded: its object stopped its replay at a journal entry it can't apply",
			"tenant", tenantID, "workload", workload, "store", what, "seq", d.Seq, "host", hostID, "error", d.Error)
	case healed:
		m.log.Info("platform store healthy again", "tenant", tenantID, "workload", workload, "store", what, "seq", prev.Seq, "applied", applied, "host", hostID)
	}
	return nil
}

// RepairStore deletes journal entry skip of a store recorded as degraded
// at skip, so every host's replay steps over it. by is who asked (audit).
func (m *Mgmt) RepairStore(ctx context.Context, tenantID, workload, kind, name string, skip int64, by string) (StoreInfo, error) {
	sj, err := m.storeDegradedJournal()
	if err != nil {
		return StoreInfo{}, err
	}
	if skip < 1 {
		return StoreInfo{}, errf(400, "bad_request", "skip must be a journal seq ≥ 1")
	}
	before, err := m.GetStore(ctx, tenantID, workload, kind, name)
	if err != nil {
		return StoreInfo{}, err
	}
	scope := before.Workload
	st, err := sj.SkipStoreEntry(ctx, tenantID, scope, kind, name, skip, m.now().UnixMilli())
	switch {
	case errors.Is(err, ErrNotFound):
		return StoreInfo{}, errf(404, "unknown_store", "workload %s has no %s store %q", workload, kind, name)
	case errors.Is(err, errStoreNotDegraded):
		if before.Degraded == nil {
			return StoreInfo{}, errf(409, "store_not_degraded", "%s store %s is not degraded: no host reports a journal entry it can't apply", kind, name)
		}
		return StoreInfo{}, errf(409, "store_not_degraded", "%s store %s is degraded at journal entry %d, not %d", kind, name, before.Degraded.Seq, skip)
	case err != nil:
		return StoreInfo{}, err
	}
	detail := map[string]any{"workload": scope, "kind": kind, "name": name, "incarnation": st.Incarnation, "seq": skip, "by": by}
	if before.Degraded != nil {
		detail["error"] = before.Degraded.Error
		detail["host"] = before.Degraded.Host
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "store_repair", ActingFor: by}, detail)
	return st, nil
}

// ListDegradedStores lists the stores recorded as degraded (tenantID "":
// all tenants).
func (m *Mgmt) ListDegradedStores(ctx context.Context, tenantID string) ([]StoreInfo, error) {
	sj, err := m.storeDegradedJournal()
	if err != nil {
		return nil, err
	}
	out, err := sj.ListDegradedStores(ctx, tenantID, degradedStoresMax)
	if out == nil {
		out = []StoreInfo{}
	}
	return out, err
}

// ---- /v1 handlers ----

func (a *v1api) ReportStoreStatus(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName, params v1.ReportStoreStatusParams) {
	var body struct {
		Incarnation string         `json:"incarnation"`
		Applied     int64          `json:"applied"`
		Degraded    *StoreDegraded `json:"degraded"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.s.M.ReportStoreStatus(r.Context(), id, workload, kind, name, hostOf(r), params.Epoch, body.Incarnation, body.Applied, body.Degraded); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) RepairStore(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, kind v1.StoreKind, name v1.StoreName) {
	var body struct {
		Skip int64 `json:"skip"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	st, err := a.s.M.RepairStore(r.Context(), id, workload, kind, name, body.Skip, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}

func (a *v1api) ListDegradedStores(w http.ResponseWriter, r *http.Request, params v1.ListDegradedStoresParams) {
	rows, err := a.s.M.ListDegradedStores(r.Context(), str(params.Tenant))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}
