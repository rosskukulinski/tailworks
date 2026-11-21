package hostd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/netd"
)

// errStatus maps catalog codes to HTTP statuses for hostd-originated errors.
var errStatus = map[string]int{
	"bad_request": 400, "no_identity": 401, "unknown_object": 404, "killed": 410,
	"identity_pending": 503, "upstream_unavailable": 503, "hostd_unavailable": 503,
	"lease_lost": 409, "namespace_conflict": 409, "spawn_budget_exceeded": 429, "bootstrap_failed": 502,
	"cap_missing": 403, "delegation_exceeds_parent": 403, "delegation_invalid": 403,
	"forbidden_destination": 403, "peer_not_visible": 403, "acl_denied": 403,
	"internet_egress_unavailable": 403,
	// Containers (ADR-032): local capacity, the tenant quota (mgmt), mgmt
	// unreachable (fail closed), a second start of a running container.
	"capacity_exhausted": 503, "container_quota_exceeded": 429, "quota_unavailable": 503, "container_running": 409,
	"tier_unavailable":     409,
	"public_create_denied": 403, "public_request": 403, "not_public": 404,
	// Create admission (docs/create-admission.md): a new name nobody admitted.
	"create_not_admitted": 403,
	"push_unauthorized":   403,
	// Approvals (approvals.go): nothing is waiting for that decision.
	"approval_not_pending": 409,
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	st := errStatus[e.Code]
	if st == 0 {
		st = 502
	}
	body := map[string]any{"code": e.Code, "message": netd.Redact(e.Detail), "detail": netd.Redact(e.Detail), "doc_url": netd.DocBase + e.Code}
	for k, v := range e.Extra {
		body[k] = v
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set(netd.HeaderError, e.Code)
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(st)
	json.NewEncoder(w).Encode(body)
}

// ingressHandler is the tenant registry's netd Upstream: netd has done
// WhoIs and set the cf blob; hostd adds cf.tw routing (only hostd can, as
// only it reaches the ingress socket) and forwards to the active workerd.
func (h *Host) ingressHandler(t *tenant) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(netd.HeaderObject)
		h.mu.Lock()
		o := h.byNetd[id]
		var wl *workload
		if o != nil {
			wl = h.workloads[o.TenantID+"/"+o.Workload]
		}
		h.mu.Unlock()
		if o == nil || wl == nil || o.TenantID != t.id || o.isPool() { // a pool node serves nothing
			writeAPIError(w, &apiError{Code: "unknown_object", Detail: "node " + id + " is not hosted here"})
			return
		}
		var blob map[string]any
		if err := json.Unmarshal([]byte(r.Header.Get(netd.HeaderCF)), &blob); err != nil || blob == nil {
			writeAPIError(w, &apiError{Code: "no_identity", Detail: "missing cf blob"})
			return
		}
		if h.serveApprovals(w, r, wl, blob) { // approvals.go: /_tw/approvals
			return
		}
		route := map[string]any{"kind": "workload", "workload": o.Workload, "objectID": o.ID}
		if o.Class != "" {
			route = map[string]any{"kind": "object", "class": o.Class, "name": o.Name, "workload": o.Workload, "objectID": o.ID}
		}
		trace := newTraceID()
		blob["tw"] = route
		blob["traceId"] = trace
		// limits.md#redeploy-wait
		ctx, cancel := context.WithTimeout(r.Context(), h.cfg.DrainTimeout+5*time.Second)
		inst, release := wl.acquire(ctx)
		cancel()
		if inst == nil {
			writeAPIError(w, &apiError{Code: "upstream_unavailable", Detail: "workload " + o.Workload + " has no running instance"})
			return
		}
		defer release()
		if !publicBlob(blob) {
			// A tailnet request may create objects: its ticket (hostd-issued,
			// hostd-verified) is what the runtime's guarded env presents to
			// "admit". Public requests get none (RT3-06/07).
			route["ticket"] = gen.CreateTicket(inst.macKey(), trace, time.Now())
		}
		b, _ := json.Marshal(blob)
		r.Header.Set(netd.HeaderCF, string(b))
		sp, ow := h.tel.startIngress(wl, inst, o, r, blob, trace, w) // telemetry.go
		inst.ingress.ServeHTTP(ow, r)
		sp.end()
	})
}

// publicBlob reports whether netd's cf blob marks the request as public
// (Funnel): tailscale.requester.kind == "public".
func publicBlob(blob map[string]any) bool {
	ts, _ := blob["tailscale"].(map[string]any)
	rq, _ := ts["requester"].(map[string]any)
	return rq["kind"] == "public"
}

// statusRecorder captures the status and tw-error of a proxied response.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// egressObjHandler serves a facet's egress (platform Egress entrypoint):
// verify the MAC, map class+name to the object (creating it on first use),
// and hand the request to netd, which dials from the object's own node.
func (h *Host) egressObjHandler(inst *instance) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cls, name, ok := gen.VerifyEgressOnce(inst.res.MACKey, r.Header, r.Method, r.RequestURI, time.Now(), &inst.nonces)
		if !ok {
			writeAPIError(w, &apiError{Code: "unknown_object", Detail: "egress request failed platform authentication"})
			return
		}
		c := inst.app.Classes[cls]
		if c == nil || !c.Facet() {
			writeAPIError(w, &apiError{Code: "unknown_object", Detail: "no facet class " + cls})
			return
		}
		o, err := h.ensure(r.Context(), inst.w, cls, name, nil, nil, "")
		if err != nil {
			writeAPIError(w, asAPIError(err))
			return
		}
		if o.Status == statusKilled {
			writeAPIError(w, &apiError{Code: "killed", Detail: "object " + o.ID + " was killed"})
			return
		}
		gen.StripPlatformHeaders(r.Header)
		h.serveEgress(w, r, inst, o)
	})
}

// egressWLHandler serves the tenant service's egress (the Worker and
// plain-DO objects): it leaves as the workload node.
func (h *Host) egressWLHandler(inst *instance) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gen.StripPlatformHeaders(r.Header)
		inst.w.mu.Lock()
		o := inst.w.wl
		inst.w.mu.Unlock()
		if o == nil {
			writeAPIError(w, &apiError{Code: "identity_pending", Detail: "workload node not created yet"})
			return
		}
		h.serveEgress(w, r, inst, o)
	})
}

func (h *Host) serveEgress(w http.ResponseWriter, r *http.Request, inst *instance, o *object) {
	r.Header.Set(netd.HeaderObject, o.NetdID)
	t := inst.w.tenant
	rec := &statusRecorder{ResponseWriter: w, code: 200}
	sp := h.tel.startEgress(inst, o, r) // telemetry.go
	t.egress.ServeHTTP(rec, r)
	sp.end(rec.code, rec.Header().Get(netd.HeaderError))
	if code := rec.Header().Get(netd.HeaderError); code != "" {
		h.audit.add(mgmtapi.AuditRecord{ObjectID: o.ID, Kind: "deny", Dst: r.URL.Host, Code: code, TraceID: newTraceID()})
	}
}

// maxTenantNotesPerCall bounds one ctx.tailnet.audit() call.
const maxTenantNotesPerCall = 64

// maxTenantNoteBytes bounds the stored note (the tenant's record, as JSON).
const maxTenantNoteBytes = 4096

// TenantAuditRecord builds the only audit record tenant code can produce
// (RT-02): kind tenant_note, source tenant, with the object, trace ID and
// acting principal assigned by the platform. The tenant's record is kept
// as the detail (truncated), including any kind/traceID/actingFor/grant it
// claimed, so a forged "allow" is visibly a note that *says* "allow".
func TenantAuditRecord(objectID, traceID, actingFor string, rec map[string]any) mgmtapi.AuditRecord {
	ar := mgmtapi.AuditRecord{ObjectID: objectID, Kind: kindTenantNote, Source: sourceTenant, TraceID: traceID, ActingFor: actingFor}
	if rec == nil {
		rec = map[string]any{}
	}
	if k, _ := rec["kind"].(string); k != "" {
		rec["claimedKind"] = k
		delete(rec, "kind")
	}
	if t, _ := rec["traceID"].(string); t != "" {
		rec["parentHint"] = t // RFC §4.10: a guest-supplied trace ID is only a hint
		delete(rec, "traceID")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		b = []byte(`{"error":"unmarshalable note"}`)
	}
	if len(b) > maxTenantNoteBytes {
		q, _ := json.Marshal(string(b[:maxTenantNoteBytes]) + "…")
		b = []byte(`{"truncated":true,"note":` + string(q) + `}`)
	}
	ar.Detail = b
	return ar
}

// ---- local host API (platform ⇄ hostd) ----

type apiReq struct {
	As *struct {
		Class string `json:"class"`
		Name  string `json:"name"`
	} `json:"as"`
	Class     string                        `json:"class"`
	Name      string                        `json:"name"`
	Caps      json.RawMessage               `json:"caps"`
	TTL       string                        `json:"ttl"`
	Wake      bool                          `json:"wake"`
	TimeoutMS int                           `json:"timeoutMs"`
	Reason    string                        `json:"reason"`
	Cascade   bool                          `json:"cascade"`
	Target    *struct{ Class, Name string } `json:"target"`
	Records   []map[string]any              `json:"records"`
	ActingFor string                        `json:"actingFor"`
	Principal string                        `json:"principal"`
	// Ticket is the create ticket of the request being served (cf.tw.ticket,
	// hostd-issued for tailnet requests and cron runs); it admits creates
	// ("admit", a workload-level "spawn"). Public requests have none.
	Ticket string `json:"ticket"`

	// Approval is a Workflow's approval wait (op "approval", approvals.go).
	Approval json.RawMessage `json:"approval"`
}

// objView snapshots o for the platform; o's fields are guarded by h.mu
// (awaitNode writes them while a request may be reading).
func (h *Host) objView(o *object) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]any{"objectID": o.ID, "objectId": o.ID, "status": o.Status, "hostname": o.Hostname, "magicDNS": o.MagicDNS,
		"kind": map[bool]string{true: "workload", false: "object"}[o.Class == ""], "class": o.Class, "name": o.Name}
}

// hostAPI serves the platform's HOSTD binding (only the trusted platform
// worker can reach it). `as` is the calling object, as attested by the
// platform (TailnetApi props); nil means the workload itself.
func (h *Host) hostAPI(inst *instance) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/container") {
			h.containerAPI(inst, w, r) // ctx.container (containers.go)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/store/") {
			h.storeAPI(inst, w, r) // platform stores (stores.go)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/queue/") {
			h.queueAPI(inst, w, r) // queue producers (queues.go)
			return
		}
		if p := r.URL.Path; p == "/v1/tail" || p == "/v1/spans" {
			h.tel.hostAPI(inst, w, r) // telemetry.go
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/r2/") {
			h.r2API(inst, w, r) // R2 bucket blobs (r2.go)
			return
		}
		var req apiReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: err.Error()})
			return
		}
		ctx := r.Context()
		wl := inst.w
		self := func() (*object, error) {
			if req.As == nil {
				wl.mu.Lock()
				defer wl.mu.Unlock()
				if wl.wl == nil {
					return nil, &apiError{Code: "identity_pending", Detail: "workload node not created yet"}
				}
				return wl.wl, nil
			}
			c := inst.app.Classes[req.As.Class]
			if c == nil || req.As.Name == "" {
				return nil, &apiError{Code: "bad_request", Detail: "unknown class " + req.As.Class}
			}
			if !c.Node {
				return nil, &apiError{Code: "bad_request", Detail: "class " + req.As.Class + " has no tailnet identity (\"node\": false)"}
			}
			return h.ensure(ctx, wl, req.As.Class, req.As.Name, nil, nil, "", req.ActingFor)
		}
		fail := func(err error) { writeAPIError(w, asAPIError(err)) }
		switch strings.TrimPrefix(r.URL.Path, "/v1/") {
		case "ensure":
			o, err := self()
			if err != nil {
				fail(err)
				return
			}
			if req.Wake {
				h.tel.wake(o)
			}
			h.mu.Lock()
			if req.Wake {
				o.Wakes++
			}
			late := req.ActingFor != "" && req.ActingFor != OriginPublic && o.ActingFor == "" && o.Class != ""
			if late {
				o.ActingFor = req.ActingFor
			}
			h.mu.Unlock()
			if late {
				h.audit.add(mgmtapi.AuditRecord{ObjectID: o.ID, Kind: "principal", ActingFor: req.ActingFor, Detail: mgmtapi.Note("acting for " + req.ActingFor), TraceID: newTraceID()})
				h.saveObjects()
			}
			writeJSON(w, 200, h.objView(o))
		case "admit":
			// May the caller reach, and so create, (class, name)? Creation is
			// never implied by reaching a binding (RT3-06/07): an attested
			// object may create (chained creation is the app's logic), a
			// tailnet request proves itself with its ticket, and a public
			// request is held to public.create (ADR-029 D5).
			var existing bool
			var e *apiError
			_, ticketOK := gen.VerifyTicket(inst.macKey(), req.Ticket, time.Now())
			if req.As != nil || ticketOK {
				existing, e = h.admitCreate(wl, inst.app, req.Class, req.Name)
			} else {
				existing, e = h.admitPublic(wl, inst.app, req.Class, req.Name)
			}
			if e != nil {
				fail(e)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "existing": existing})
		case "self":
			o, err := self()
			if err != nil {
				fail(err)
				return
			}
			v := h.objView(o)
			v["epoch"] = wl.epochNow() // the lease epoch: fencing tokens (docs/locks.md)
			writeJSON(w, 200, v)
		case "spawn":
			c := inst.app.Classes[req.Class]
			if c == nil || req.Name == "" || !c.Node {
				fail(&apiError{Code: "bad_request", Detail: "spawn: unknown class " + req.Class + " (or it has no node)"})
				return
			}
			parent, err := self()
			if err != nil {
				fail(err)
				return
			}
			if len(req.Caps) == 0 || string(req.Caps) == "null" {
				req.Caps = nil
			}
			ttl := req.TTL
			if ttl == "" {
				ttl = c.TTL
			}
			if parent.Class == "" {
				// A spawn from the Worker (workload identity) is admitted by the
				// request's ticket; a public request has none, and neither has
				// code that reached __TW some other way (RT3-07).
				if _, ok := gen.VerifyTicket(inst.macKey(), req.Ticket, time.Now()); !ok {
					fail(&apiError{Code: "create_not_admitted", Detail: "spawn from the workload needs the request's create ticket (a public request may not spawn)"})
					return
				}
				h.admitLocal(wl, req.Class, req.Name)
			}
			// The child acts for its parent's principal (RFC §4.6); tenant code
			// chooses only "inherit" (default) or "none", never a name (RT3-08).
			af := parent.ActingFor
			if req.Principal == "none" {
				af = ""
			}
			o, err := h.ensure(ctx, wl, req.Class, req.Name, parent, req.Caps, ttl, af)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, 200, h.objView(o))
		case "ready":
			c := inst.app.Classes[req.Class]
			if c == nil || !c.Node {
				fail(&apiError{Code: "bad_request", Detail: "ready: unknown class " + req.Class})
				return
			}
			// ready waits for an object spawn already created; it never creates.
			h.mu.Lock()
			o := h.byKey[objKey(wl.tenantID, wl.name, req.Class, req.Name)]
			h.mu.Unlock()
			if o == nil {
				fail(&apiError{Code: "unknown_object", Detail: "no live object " + req.Class + "/" + req.Name + " (ready follows spawn)"})
				return
			}
			to := time.Duration(req.TimeoutMS) * time.Millisecond
			// limits.md#ready-wait-max
			if to <= 0 || to > 5*time.Minute {
				to = h.cfg.ReadyTimeout
			}
			if err := h.waitReady(ctx, o, to); err != nil {
				fail(err)
				return
			}
			writeJSON(w, 200, h.objView(o))
		case "kill":
			caller, err := self()
			if err != nil {
				fail(err)
				return
			}
			target := caller
			if req.Target != nil {
				h.mu.Lock()
				target = h.byKey[objKey(wl.tenantID, wl.name, req.Target.Class, req.Target.Name)]
				h.mu.Unlock()
				if target == nil {
					fail(&apiError{Code: "unknown_object", Detail: "no live object " + req.Target.Class + "/" + req.Target.Name})
					return
				}
				// Self or own descendants only (the workload may kill its objects).
				if req.As != nil && target != caller && !h.isDescendant(target, caller) {
					fail(&apiError{Code: "delegation_invalid", Detail: "an object may kill only itself or its descendants"})
					return
				}
			}
			reason := req.Reason
			if reason == "" {
				reason = "killed by " + caller.Hostname
			}
			resp, err := h.mgmt.Kill(ctx, target.ID, mgmtapi.KillRequest{Cascade: req.Cascade, Reason: reason})
			if err != nil {
				fail(err)
				return
			}
			// mgmt pushes the kill too; do it here as well so it is immediate.
			go func() {
				for _, id := range resp.Killed {
					h.killLocal(id, reason)
				}
			}()
			writeJSON(w, 200, map[string]any{"killed": resp.Killed})
		case "audit":
			o, err := self()
			if err != nil {
				fail(err)
				return
			}
			// RT-02: tenant code can only add tenant_note records. netd/hostd
			// assign the trace ID, the acting principal, the object and the
			// timestamp; whatever the tenant sent is kept verbatim as the
			// note, never as a platform decision.
			n := 0
			for _, rec := range req.Records {
				if n >= maxTenantNotesPerCall {
					break
				}
				if !h.audit.add(TenantAuditRecord(o.ID, newTraceID(), o.ActingFor, rec)) {
					break // over the object's note bucket, or the queue is backed up (H03)
				}
				n++
			}
			writeJSON(w, 200, map[string]any{"ok": true, "accepted": n, "kind": kindTenantNote})
		case "approval":
			out, e := h.approvalReport(inst, req.Approval)
			if e != nil {
				fail(e)
				return
			}
			writeJSON(w, 200, out)
		default:
			fail(&apiError{Code: "bad_request", Detail: "unknown op"})
		}
	})
}

// ---- push API (mgmt → hostd) and status ----

// pushHandlers registers the pushes mgmt sends a host: kills and
// assignment changes. They are served on both push surfaces.
func (h *Host) pushHandlers(m *http.ServeMux) {
	m.HandleFunc("POST /v1/push/kill", func(w http.ResponseWriter, r *http.Request) {
		var k mgmtapi.KillOrder
		if err := json.NewDecoder(r.Body).Decode(&k); err != nil || k.ObjectID == "" {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: "objectID required"})
			return
		}
		if err := h.killLocal(k.ObjectID, k.Reason); err != nil {
			// The node is cut off, but workerd did not confirm the in-process
			// kill: not a pushed kill (mgmt reports it; the next heartbeat retries).
			writeAPIError(w, &apiError{Code: "hostd_unavailable", Detail: err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	m.HandleFunc("POST /v1/push/assignments", func(w http.ResponseWriter, r *http.Request) {
		h.TriggerSync()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	// Smart placement (ADR-033): measure a tenant's targets now (mgmt, at deploy).
	m.HandleFunc("POST /v1/push/probe", h.probeHandler)
	// upgrade.go: install a release pushed by an upgrade run (Config.SelfUpgrade).
	m.HandleFunc("POST /v1/push/upgrade", h.upgradeHandler)
	// logs.go: one workload's app log, for `tw logs` from a linked machine
	// (mgmt asks the lease holder; on the tsnet surface pushAuth admits
	// mgmt's node only).
	m.HandleFunc("GET /v1/logs", h.logsHandler)
	// storeadmin.go: an operator op on a platform store (tw d1 execute, …).
	m.HandleFunc("POST /v1/store-admin", h.storeAdminHandler)
	// approvals.go: tw approvals / tw approve and the admin UI, through mgmt.
	m.HandleFunc("GET /v1/approvals", h.approvalsListHandler)
	m.HandleFunc("POST /v1/approvals/decide", h.approvalsDecideHandler)
}

// tsnetPushAPI is what the host's own tailnet node serves (Config.
// PushListener): the pushes only, each authenticated as coming from mgmt's
// node (pushAuth). Status, purge and VM checks are the operator's and stay
// on the local socket.
func (h *Host) tsnetPushAPI() http.Handler {
	m := http.NewServeMux()
	h.pushHandlers(m)
	return h.pushAuth.handler(m)
}

// PushHandler is the push API (POST /v1/push/kill, POST /v1/push/assignments)
// for mgmt running in the same process (Config.PushInProcess): mgmt serves
// each push through it directly, so there is no peer to authenticate. It
// routes nothing else (status and purge stay on the local socket).
func (h *Host) PushHandler() http.Handler {
	m := http.NewServeMux()
	h.pushHandlers(m)
	return m
}

// pushAPI is the local unix-socket API (<run-dir>/hostd.sock, mode 0600,
// procsb.Guard): the pushes (local-dev mgmt) plus the operator's status,
// purge and VM checks (tw status / down --purge / host vmcheck).
func (h *Host) pushAPI() http.Handler {
	m := http.NewServeMux()
	h.pushHandlers(m)
	// tw down --purge: forget killed objects (their tombstones) so a
	// redeploy on this state dir starts from a clean registry.
	m.HandleFunc("POST /v1/purge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "purged": h.Purge()})
	})
	m.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, h.Status())
	})
	// The operator's local scrape (the tailnet one is MetricsListener).
	m.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) { h.MetricsHandler().ServeHTTP(w, r) })
	// teardown.go: no new objects before tw down --purge lists them.
	m.HandleFunc("POST /v1/teardown", h.teardownHandler)

	// Operator checks of a workload's microVM (tw host vmcheck): guest
	// stats, DO volume fsync latency, and the no-keys-in-guest scan. The
	// socket is the user's (0700 dir); answers carry no key material.
	m.HandleFunc("POST /v1/vm/check", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if ct := q.Get("container"); ct != "" { // <Class>/<name>: that object's container guest
			cls, name, ok := strings.Cut(ct, "/")
			if !ok || q.Get("keyscan") != "1" {
				writeAPIError(w, &apiError{Code: "bad_request", Detail: "container=<Class>/<name> needs keyscan=1"})
				return
			}
			rep, err := h.ContainerCheck(r.Context(), q.Get("tenant"), q.Get("workload"), cls, name)
			if err != nil {
				writeAPIError(w, &apiError{Code: "bad_request", Detail: err.Error()})
				return
			}
			writeJSON(w, 200, rep)
			return
		}
		rep, err := h.VMCheck(r.Context(), q.Get("tenant"), q.Get("workload"), q.Get("keyscan") == "1")
		if err != nil {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: err.Error()})
			return
		}
		writeJSON(w, 200, rep)
	})
	return m
}

// ObjectStatus is a status view of one object.
type ObjectStatus struct {
	ID, NetdID, TenantID, Workload, Class, Name, Hostname, Status, NodeID, MagicDNS, IP, Error, Parent string
	Wakes                                                                                              int
}

// WorkloadStatus is a status view of one workload.
type WorkloadStatus struct {
	TenantID, Workload, DeploymentID, Status, Error string
	Epoch                                           int64
	Requests                                        int64
	InstanceDir                                     string
	Tier                                            string
	// Crons are the active deployment's cron triggers (UTC).
	Crons []string      `json:",omitempty"`
	VM    *sandbox.Info `json:",omitempty"`
	// VMRSSKB is the microVM's VMM resident set (incl. hugetlb guest RAM).
	VMRSSKB int64 `json:",omitempty"`
	// Process tier: the workerd (or node) pid, its sandbox ("none" |
	// "seatbelt" | "landlock"), the profile (.sb / .json), and its DO
	// storage dir.
	PID            int    `json:",omitempty"`
	Sandbox        string `json:",omitempty"`
	SandboxProfile string `json:",omitempty"`
	// LoopbackPorts are hostd's token-checked loopback listeners for this
	// workerd's outbound services (Landlock only).
	LoopbackPorts []uint16 `json:",omitempty"`
	DOStorage     string   `json:",omitempty"`
	// Runtime is "node" for the Node process tier ("" = workerd).
	Runtime string    `json:",omitempty"`
	Node    *NodeInfo `json:",omitempty"`
	// Service is the workload's Tailscale Service and whether this host
	// advertises it now.
	Service *ServiceStatus `json:",omitempty"`
	// Egress (Node tier) says whether egress attribution is an enforcement
	// boundary on this host: "enforced" only under a sandbox that confines
	// the process's network to the egress proxy (Landlock ABI ≥ 4 on Linux,
	// Seatbelt on macOS, or a microVM); otherwise "unenforced": the proxy is cooperative and a
	// hostile app can bypass it with raw sockets (RT-08, docs/node-tier.md).
	Egress string `json:",omitempty"`
	// LogFile is the app's log (workerd.log, or app.log for runtime node).
	LogFile string `json:",omitempty"`
	// RequestLog is the workload's structured request records
	// (ADR-045; JSON Lines), whether or not it exists yet.
	RequestLog string `json:",omitempty"`
	// Public is the workload's public ingress (ADR-029), if any.
	Public *PublicStatus `json:",omitempty"`

	// Restarts counts the workerd's exits in a row (restart.go; reset after
	// it ran 30 s). Status is "crashloop" from the second.
	Restarts int `json:",omitempty"`
}

// PublicStatus is a workload's public node as this host runs it.
type PublicStatus struct {
	ObjectID, Hostname, MagicDNS, Status string
	Exposed                              bool
	Routes                               []string
}

// Egress enforcement states (WorkloadStatus.Egress).
const (
	EgressEnforced   = "enforced"
	EgressUnenforced = "unenforced"
)

// EgressEnforcement reports whether a Node-tier process under the given
// sandbox can only reach the network through hostd's egress proxy. macOS
// Seatbelt does (TCP connects only to localhost:<proxy> and its own port);
// "none" and "seatbelt-legacy" don't. The Linux Landlock sandbox pins the process's TCP connects to the proxy port
// only on a kernel with Landlock ABI ≥ 4 (6.7+). Below that, Landlock has
// no TCP rules and the seccomp filter allows TCP sockets, so the proxy is
// cooperative there too. A microVM is the only other enforcement boundary.
func EgressEnforcement(sandbox string) string {
	return egressEnforcement(sandbox, landlockABI(sandbox))
}

// StatusView is GET /v1/status (hostd's local API).
type StatusView struct {
	HostID string
	// Sandbox is the host's process sandbox: "seatbelt", "landlock (ABI n)"
	// or "none".
	Sandbox string `json:",omitempty"`
	// Tiers this host offers ("process", and "microvm" with a backend);
	// VMVia says how microVMs are launched (tw-vmhelper or in-process).
	Tiers     []string
	VMVia     string `json:",omitempty"`
	Objects   []ObjectStatus
	Workloads []WorkloadStatus
	// InsecureSharedHost echoes Config.InsecureSharedHost.
	InsecureSharedHost bool `json:",omitempty"`
	// Containers are this host's containers (ADR-032), running and stopped.
	Containers []ContainerStatus `json:",omitempty"`
	// ImageCacheBytes is the converted container image cache on disk.
	ImageCacheBytes int64 `json:",omitempty"`
}

// Status returns a snapshot.
func (h *Host) Status() StatusView {
	h.mu.Lock()
	v := StatusView{HostID: h.cfg.HostID, Sandbox: h.SandboxInfo(), Tiers: h.tiers(), InsecureSharedHost: h.cfg.InsecureSharedHost}
	if h.cfg.VM != nil {
		if vv, ok := h.cfg.VM.(interface{ Via() string }); ok {
			v.VMVia = vv.Via()
		}
	}
	for _, o := range h.objects {
		v.Objects = append(v.Objects, ObjectStatus{o.ID, o.NetdID, o.TenantID, o.Workload, o.Class, o.Name, o.Hostname, o.Status, o.NodeID, o.MagicDNS, o.IP, o.Err, o.Parent, o.Wakes})
	}
	wls := make([]*workload, 0, len(h.workloads))
	pubs := map[string]*PublicStatus{}
	for k, w := range h.workloads {
		wls = append(wls, w)
		if ps := h.public[k]; ps != nil && ps.spec != nil {
			st := &PublicStatus{Exposed: ps.spec.Exposed, Routes: ps.spec.Routes, Status: "pending"}
			if o := h.byKey[objKey(w.tenantID, w.name, mgmtapi.PublicNamespace, w.name)]; o != nil {
				st.ObjectID, st.Hostname, st.MagicDNS, st.Status = o.ID, o.Hostname, o.MagicDNS, o.Status
			}
			pubs[k] = st
		}
	}
	h.mu.Unlock()
	for _, w := range wls {
		w.mu.Lock()
		ws := WorkloadStatus{TenantID: w.tenantID, Workload: w.name, DeploymentID: w.activeDep, Status: w.status, Error: w.err, Epoch: w.epoch,
			Public: pubs[w.tenantID+"/"+w.name]}
		ws.Service = w.serviceStatus()
		if w.active != nil {
			ws.Requests = w.active.reqs.Load()
			ws.InstanceDir = w.active.dir
			ws.Tier = w.active.tier
			if w.active.app != nil {
				ws.Crons = w.active.app.Crons
			}
			ws.LogFile = filepath.Join(w.active.dir, "workerd.log")
			ws.RequestLog = h.RequestLogPath(w.tenantID, w.name)
			if w.active.node != nil {
				ws.Runtime, ws.Node, ws.LogFile = bundle.RuntimeNode, w.active.nodeInfo(), w.active.node.logPath
			}
			ws.DOStorage = w.active.doStorage
			w.active.mu.Lock()
			if w.active.cmd != nil && w.active.cmd.Process != nil {
				ws.PID = w.active.cmd.Process.Pid
			}
			ws.Restarts = w.active.restarts
			if w.active.tier != "microvm" {
				ws.Sandbox, ws.SandboxProfile = sandboxOf(w.active.profile), w.active.profile
				ws.LoopbackPorts = w.active.loopPorts
			}
			if ws.Runtime == bundle.RuntimeNode {
				ws.Egress = EgressEnforcement(ws.Sandbox)
			}
			w.active.mu.Unlock()
			if vm := w.active.currentVM(); vm != nil {
				info := vm.Info()
				ws.VM = &info
				ws.VMRSSKB = vm.RSSKB()
				if info.Console != "" {
					ws.LogFile = info.Console // guest workerd logs to the serial console
				}
			}
		}
		w.mu.Unlock()
		v.Workloads = append(v.Workloads, ws)
	}
	sort.Slice(v.Objects, func(i, j int) bool { return v.Objects[i].Hostname < v.Objects[j].Hostname })
	if h.ctr != nil {
		v.Containers = h.Containers()
	}
	if h.cfg.Images != nil {
		v.ImageCacheBytes = h.cfg.Images.CacheBytes()
	}
	sort.Slice(v.Workloads, func(i, j int) bool { return v.Workloads[i].Workload < v.Workloads[j].Workload })
	return v
}

// Sandbox returns the running microVM of a workload's active instance (nil
// in the process tier).
func (h *Host) Sandbox(tenantID, workload string) sandbox.VM {
	h.mu.Lock()
	w := h.workloads[tenantID+"/"+workload]
	h.mu.Unlock()
	if w == nil {
		return nil
	}
	inst := w.current()
	if inst == nil {
		return nil
	}
	return inst.currentVM()
}

// Object returns the status of the object (class, name) of a workload.
func (h *Host) Object(tenantID, workload, class, name string) (ObjectStatus, bool) {
	for _, o := range h.Status().Objects {
		if o.TenantID == tenantID && o.Workload == workload && o.Class == class && o.Name == name && o.Status != statusKilled {
			return o, true
		}
	}
	return ObjectStatus{}, false
}
