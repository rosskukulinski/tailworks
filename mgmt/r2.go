package mgmt

// R2 buckets, operator view (ADR-036 §8): `tw r2 object ls` lists a
// bucket's live objects through GET …/stores/r2/{name}/objects. A bucket's
// index is a platform store of kind "r2" (ADR-044) that only the
// platform's store-r2.js writes; mgmt folds its journal (internal/r2wire)
// so the CLI doesn't page the raw journal and re-implement the fold.
//
// The fold streams: each journal page is applied and dropped, and only
// objects inside the request's window (prefix, after the cursor) are held.
// The journal is compacted by key, so a read is the size of the live
// index, not of its history.

import (
	"context"
	"errors"
	"net/http"

	"github.com/rosskukulinski/tailworks/internal/r2wire"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// ListR2Objects returns a bucket's live objects whose keys start with
// prefix and sort after after, in key order.
func (m *Mgmt) ListR2Objects(ctx context.Context, tenantID, workload, name, prefix, after string) ([]r2wire.Object, error) {
	st, err := m.GetStore(ctx, tenantID, workload, r2wire.Kind, name)
	if err != nil {
		return nil, err
	}
	f := &r2wire.Fold{Prefix: prefix, After: after}
	var seq int64
	for {
		p, err := m.ReadStore(ctx, tenantID, workload, r2wire.Kind, name, "", nil, seq, 0)
		if err != nil {
			return nil, err
		}
		if p.Incarnation != st.Incarnation {
			return nil, errf(409, "store_conflict", "R2 bucket %s was deleted or recreated during the read; retry", name)
		}
		for _, e := range p.Entries {
			if err := f.Add(e.Data); err != nil {
				if errors.Is(err, r2wire.ErrNewer) {
					return nil, errf(409, "version_skew", "R2 bucket %s: %v", name, err)
				}
				return nil, errf(500, "internal", "R2 bucket %s seq %d: %v", name, e.Seq, err)
			}
			seq = e.Seq
		}
		if !p.More || len(p.Entries) == 0 {
			break
		}
	}
	return f.Objects(), nil
}

// ---- /v1 handler ----

func (a *v1api) ListR2Objects(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.StoreName, params v1.ListR2ObjectsParams) {
	w.Header().Set("Cache-Control", "no-store")
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	after := ""
	if pg.After != nil {
		after = pg.After.ID
	}
	objs, err := a.s.M.ListR2Objects(r.Context(), id, workload, name, str(params.Prefix), after)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, mkPage(objs, n, func(o r2wire.Object) PageKey { return PageKey{ID: o.Key} }))
}
