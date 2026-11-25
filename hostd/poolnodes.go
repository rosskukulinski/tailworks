package hostd

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// Pre-joined node pool (ADR-031, variant B: one pool per host, tenant,
// workload and class, so a pool node already carries exactly the class's
// tags). A pool node is an mgmt object in namespace "_pool" that joins like
// any object (mint, fenced credential, ready) and serves nothing. A create
// of that class offers one to mgmt (CreateObject.poolObjectID); if mgmt
// assigns it, the new object inherits the pool node's netd node and this
// host renames it (netd SetHostname: the node's own LocalAPI, ~0.2 s)
// instead of registering a new node (1.3 s p50, with multi-second control
// tails), then reports ready on the same stable node ID.
//
// Only classes with a root create on this host in the last hour get a pool
// (the "hot" ones), at most Config.PoolNodes nodes each.

// PoolClass is mgmt's pool namespace; tenant code can never address it.
const PoolClass = "_pool"

const poolDemandWindow = time.Hour

// poolable reports whether objects of class may use (and warm) a pool: DO
// classes only, never the workload entry node, a pool node or the public
// node (reserved namespaces start with "_"; the public one is checked too).
func poolable(class string) bool {
	return class != "" && !strings.HasPrefix(class, "_") && class != mgmtapi.PublicNamespace
}

func poolKey(tenantID, workload, class string) string { return tenantID + "/" + workload + "/" + class }

func randSuffix() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// isPool reports whether o is a pool node.
func (o *object) isPool() bool { return o.Class == PoolClass }

// poolClassOf is the class a pool node is for ("<Class>/<suffix>").
func (o *object) poolClassOf() string { c, _, _ := strings.Cut(o.Name, "/"); return c }

// rejoinsWith reports whether a saved object of a workload rejoins from
// saved state when app starts: the workload's entry node, an object of a
// class that has its own node, and a pool node warmed for such a class. A
// pool node's Class is PoolClass, never one of app's, so without the last
// case an idle pool node stayed offline after a host restart while mgmt
// still listed it as ready (ADR-031: a restart re-uses pool nodes).
func rejoinsWith(o *object, app *bundle.App) bool {
	class := o.Class
	if o.isPool() {
		if class = o.poolClassOf(); class == "" {
			return false
		}
	}
	if class == "" {
		return true
	}
	c := app.Classes[class]
	return c != nil && c.Node
}

// takePoolLocked reserves a ready, idle pool node for (tenant, workload,
// class), or returns nil. h.mu must be held.
func (h *Host) takePoolLocked(tenantID, workload, class string) *object {
	if h.cfg.PoolNodes <= 0 || !poolable(class) {
		return nil
	}
	for _, p := range h.objects {
		if p.isPool() && !p.reserved && p.live && p.Status == statusReady && p.TenantID == tenantID && p.Workload == workload && p.poolClassOf() == class {
			p.reserved = true
			return p
		}
	}
	return nil
}

// noteDemand records a create of class on this host (sizing).
func (h *Host) noteDemand(tenantID, workload, class string) {
	if h.cfg.PoolNodes <= 0 || !poolable(class) {
		return
	}
	h.mu.Lock()
	h.poolDemand[poolKey(tenantID, workload, class)] = time.Now()
	h.mu.Unlock()
	go h.maintainPools()
}

// maintainPools tops up every hot class's pool to Config.PoolNodes, one new
// node per class at a time (each join is a registration: ADR-017, and
// mgmt's throttle applies to its mint as to any object's).
func (h *Host) maintainPools() {
	if h.cfg.PoolNodes <= 0 || h.ctx.Err() != nil {
		return
	}
	type want struct {
		w     *workload
		class string
		key   string
	}
	var todo []want
	h.mu.Lock()
	if h.tearingDown { // teardown.go: tw down --purge
		h.mu.Unlock()
		return
	}
	for key, at := range h.poolDemand {
		if time.Since(at) > poolDemandWindow {
			delete(h.poolDemand, key)
			continue
		}
		if h.poolBusy[key] {
			continue
		}
		parts := strings.SplitN(key, "/", 3)
		w := h.workloads[parts[0]+"/"+parts[1]]
		if w == nil || w.tenant == nil || !w.isActive() {
			continue
		}
		n := 0
		for _, p := range h.objects {
			if p.isPool() && !p.reserved && p.live && !p.terminal() && p.Status != statusFailed &&
				p.TenantID == parts[0] && p.Workload == parts[1] && p.poolClassOf() == parts[2] {
				n++
			}
		}
		if n < h.cfg.PoolNodes {
			h.poolBusy[key] = true
			todo = append(todo, want{w, parts[2], key})
		}
	}
	// Retire idle pool nodes of classes gone cold: no create here for
	// poolDemandWindow, and the node itself at least that old (so a restart,
	// which forgets demand, doesn't throw a warm pool away). Each is a
	// device; the kill is mgmt's (audited, device deleted).
	retire := h.idlePoolsLocked(time.Now())
	for _, p := range retire {
		p.reserved = true // never handed out while it is being killed
	}
	h.mu.Unlock()
	for _, p := range retire {
		go func() {
			ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
			defer cancel()
			if _, err := h.mgmt.Kill(ctx, p.ID, mgmtapi.KillRequest{Reason: "pool node idle"}); err != nil {
				h.log.Warn("idle pool node not retired", "object", p.ID, "err", err)
				h.mu.Lock()
				p.reserved = false
				h.mu.Unlock()
				return
			}
			h.killLocal(p.ID, "pool node idle")
			h.log.Info("idle pool node retired", "object", p.ID, "class", p.poolClassOf())
		}()
	}
	for _, x := range todo {
		go func() {
			defer func() {
				h.mu.Lock()
				delete(h.poolBusy, x.key)
				h.mu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
			defer cancel()
			o, err := h.create(ctx, x.w, PoolClass, x.class+"/"+randSuffix(), nil, nil, "", "")
			if err != nil {
				h.log.Warn("pool node not created", "tenant", x.w.tenantID, "workload", x.w.name, "class", x.class, "err", err)
				return
			}
			h.log.Info("pool node joining", "object", o.ID, "class", x.class, "hostname", o.Hostname)
		}()
	}
}

// assignNode finishes a create that mgmt answered "assigned": o already
// owns the pool node's netd node; rename it and report ready on it.
func (h *Host) assignNode(o *object, from string) {
	t0 := time.Now()
	h.mu.Lock()
	t := h.tenants[o.TenantID]
	ch := o.readyCh
	h.mu.Unlock()
	fail := func(why string, err error) {
		h.log.Warn("pool node assignment failed", "object", o.ID, "pool", from, "why", why, "err", err)
		h.mu.Lock()
		o.Status, o.Err = statusFailed, why
		h.mu.Unlock()
		closeOnce(ch)
	}
	if t == nil {
		fail("tenant not on this host", nil)
		return
	}
	no := t.reg.Get(o.NetdID)
	if no == nil {
		fail("pool node gone from netd", nil)
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	defer cancel()
	dns, err := no.SetHostname(ctx, o.Hostname)
	if err != nil {
		// The node is the object's either way (bound by its stable ID);
		// report the name control actually gave it.
		h.log.Warn("pool node rename not confirmed; reporting its current name", "object", o.ID, "err", err)
	}
	info := no.Info()
	if dns == "" {
		dns = info.DNSName
	}
	h.mu.Lock()
	if o.Status == statusKilled {
		h.mu.Unlock()
		return
	}
	o.NodeID, o.MagicDNS, o.Status, o.Err = info.NodeID, strings.TrimSuffix(dns, "."), statusReady, ""
	if len(info.IPs) > 0 {
		o.IP = info.IPs[0]
	}
	gen := o.nodeGen
	h.mu.Unlock()
	var epoch int64
	if w := h.workloadOf(o); w != nil {
		epoch = w.epochNow()
	}
	req := mgmtapi.ReadyRequest{NodeID: info.NodeID, StableNodeID: info.NodeID, MagicDNS: o.MagicDNS, Epoch: epoch}
	err = h.mgmt.Ready(ctx, o.ID, req)
	h.noteReport(o, info.NodeID, err) // egressgate.go (#331)
	if err != nil {
		h.log.Warn("mgmt ready failed", "object", o.ID, "err", err)
		if e, ok := mgmtapi.AsError(err); ok && e.Code == mgmtapi.CodeKilled {
			closeOnce(ch)
			go h.killLocal(o.ID, "killed during assignment")
			return
		}
		if retryableReady(err) {
			go h.retryReady(o, req, gen) // readyretry.go (#604)
		}
	}
	closeOnce(ch)
	h.saveObjects()
	h.log.Info("object node ready", "object", o.ID, "hostname", o.Hostname, "ip", o.IP, "from_pool", from,
		"assign_ms", time.Since(t0).Milliseconds(), "rejoined", false, "reminted", false)
}

// idlePoolsLocked lists the pool nodes to retire at now: not reserved, not
// dead, at least poolDemandWindow old, and of a class with no create here
// in that window. h.mu must be held.
func (h *Host) idlePoolsLocked(now time.Time) []*object {
	var out []*object
	for _, p := range h.objects {
		if !p.isPool() || p.reserved || p.terminal() || now.Sub(p.Created) < poolDemandWindow {
			continue
		}
		if at, hot := h.poolDemand[poolKey(p.TenantID, p.Workload, p.poolClassOf())]; !hot || now.Sub(at) > poolDemandWindow {
			out = append(out, p)
		}
	}
	return out
}
