package hostd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/r2"
)

// Deleted tenants (issue #107): mgmt lists a deleted tenant in the
// heartbeat reply (PurgeTenants) until this host reports it purged
// (PurgedTenants). Purging stops everything of the tenant still here,
// closes its netd registry, and removes tenants/<id>/ (node state, DO
// storage and volumes, logs) and the tenant's R2 blobs. It is idempotent,
// so a hostd that restarts part way simply does it again.

// tenantPurgeState tracks purges in flight and done (zero value ready).
type tenantPurgeState struct {
	mu      sync.Mutex
	running map[string]bool
	purged  map[string]bool // done, not yet acknowledged by mgmt
}

// done lists the purged tenants to report in the next heartbeat.
func (s *tenantPurgeState) done() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.purged))
	for id := range s.purged {
		out = append(out, id)
	}
	return out
}

// purgeTenants starts the purge of each listed tenant not already purging.
// mgmt stops listing a tenant once it has the report, which is when its
// report is dropped here.
func (h *Host) purgeTenants(ids []string) {
	s := &h.tenantPurge
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.purged {
		if !want[id] {
			delete(s.purged, id) // acknowledged
		}
	}
	for _, id := range ids {
		if s.running[id] || s.purged[id] {
			continue
		}
		if s.running == nil {
			s.running = map[string]bool{}
		}
		s.running[id] = true
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			err := h.purgeTenant(id)
			s.mu.Lock()
			delete(s.running, id)
			if err == nil {
				if s.purged == nil {
					s.purged = map[string]bool{}
				}
				s.purged[id] = true
			}
			s.mu.Unlock()
		}()
	}
}

// purgeTenant removes everything of tenant id on this host.
func (h *Host) purgeTenant(id string) error {
	if !tenantIDRE.MatchString(id) || strings.Contains(id, "..") {
		h.log.Error("refusing to purge a tenant whose ID is not a safe path element", "tenant", id)
		return errors.New("unsafe tenant ID")
	}
	st := time.Now()
	h.mu.Lock()
	var objs []string
	for _, o := range h.objects {
		if o.TenantID == id {
			objs = append(objs, o.ID)
		}
	}
	var wls []*workload
	for k, w := range h.workloads {
		if w.tenantID == id {
			wls = append(wls, w)
			delete(h.workloads, k)
		}
	}
	h.mu.Unlock()
	for _, oid := range objs {
		h.killLocal(oid, "tenant deleted")
	}
	for _, w := range wls {
		h.undeploy(w, "tenant deleted")
	}
	h.dropTenantAdmissions(id) // admitkill.go (#153)
	h.mu.Lock()
	for _, o := range h.objects {
		if o.TenantID == id {
			h.forgetLocked(o)
		}
	}
	t := h.tenants[id]
	delete(h.tenants, id)
	h.mu.Unlock()
	h.saveObjects()
	if t != nil && t.reg != nil {
		t.reg.Close()
	}
	if err := os.RemoveAll(h.tenantDir(id)); err != nil {
		h.log.Error("deleted tenant's files not removed; retrying at the next heartbeat", "tenant", id, "err", err)
		return err
	}
	if err := h.purgeTenantR2(id); err != nil {
		h.log.Error("deleted tenant's R2 blobs not removed; retrying at the next heartbeat", "tenant", id, "err", err)
		return err
	}
	h.log.Info("deleted tenant purged from this host", "tenant", id, "objects", len(objs), "workloads", len(wls), "ms", time.Since(st).Milliseconds())
	return nil
}

// tenantR2Purger is a blob backend that can delete a tenant's blobs.
type tenantR2Purger interface {
	DeleteTenant(ctx context.Context, tenant string) error
}

// purgeTenantR2 removes the tenant's R2 blobs (r2.Name: "t/<tenant>/…").
func (h *Host) purgeTenantR2(id string) error {
	switch b := h.cfg.R2.(type) {
	case nil:
		return nil
	case *r2.FS:
		return os.RemoveAll(filepath.Join(b.Root, "t", id))
	case tenantR2Purger:
		ctx, cancel := context.WithTimeout(h.ctx, 5*time.Minute)
		defer cancel()
		return b.DeleteTenant(ctx, id)
	default:
		h.log.Warn("deleted tenant's R2 blobs stay in the operator's bucket (this backend cannot list them); delete the prefix by hand",
			"tenant", id, "backend", b.String(), "prefix", "t/"+id+"/")
		return nil
	}
}
