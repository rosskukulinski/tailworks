package mgmt

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// v1api implements the generated v1.ServerInterface (api/openapi.yaml is
// the source of truth; mgmt/api/v1 is generated from it). Routing and
// parameter parsing are generated; this file holds the glue to Mgmt and
// the /v1 conventions: pages, ETags, merge patches, tombstones.
type v1api struct {
	s *Server
}

var _ v1.ServerInterface = (*v1api)(nil)

// ---- conventions ----

// etagOf is the strong validator of a representation.
func etagOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// writeResource writes v with its ETag, honouring If-None-Match.
func writeResource(w http.ResponseWriter, r *http.Request, status int, v any) {
	et := etagOf(v)
	w.Header().Set("ETag", et)
	if inm := r.Header.Get("If-None-Match"); inm != "" && status == 200 && etagMatches(inm, et) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, status, v)
}

func etagMatches(header, et string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "W/"))
		if p == "*" || p == et {
			return true
		}
	}
	return false
}

// checkIfMatch enforces If-Match against the current representation.
func checkIfMatch(r *http.Request, current any) error {
	im := r.Header.Get("If-Match")
	if im == "" {
		return nil
	}
	et := etagOf(current)
	if !etagMatches(im, et) {
		e := errf(412, "precondition_failed", "If-Match %s does not match the current ETag %s", im, et)
		return e
	}
	return nil
}

// pageParams turns limit/cursor into a store Page that fetches one extra
// row (to know whether there is a next page).
func pageParams(limit *int, cursor *string) (Page, int, error) {
	n := PageDefaultLimit
	if limit != nil {
		if *limit <= 0 || *limit > PageMaxLimit {
			return Page{}, 0, errf(400, "bad_request", "limit must be between 1 and %d", PageMaxLimit)
		}
		n = *limit
	}
	pg := Page{Limit: n + 1}
	if cursor != nil {
		k, err := DecodeCursor(*cursor)
		if err != nil {
			return pg, n, errf(400, "bad_cursor", "cursor does not decode; start again without it")
		}
		pg.After = k
	}
	return pg, n, nil
}

type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func mkPage[T any](rows []T, limit int, key func(T) PageKey) page[T] {
	items, next := pageOf(rows, limit, key)
	if items == nil {
		items = []T{}
	}
	return page[T]{Items: items, NextCursor: next}
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolOf(p *bool) bool { return p != nil && *p }

func location(w http.ResponseWriter, path string) { w.Header().Set("Location", path) }

// requireLease is the host-role object scope (M2): a host principal may
// touch an object only while it holds the object's (tenant, workload)
// lease; admins and the local-dev socket pass. The epoch fence in the
// service is on top of this.
func (a *v1api) requireLease(r *http.Request, lease string) error {
	p := principalOf(r.Context())
	if p.Kind != "host" {
		return nil
	}
	if l, err := a.s.M.store.GetLease(r.Context(), lease); err != nil || l.Holder != p.HostID {
		return errf(403, "forbidden", "host %s does not hold the lease %s", p.HostID, lease)
	}
	return nil
}

// hostScope is what a host principal may read (M3): the leases it holds
// and the tenants those deployments belong to. isHost is false for every
// other principal (they see everything).
func (a *v1api) hostScope(r *http.Request) (leases, tenants map[string]bool, isHost bool) {
	p := principalOf(r.Context())
	if p.Kind != "host" {
		return nil, nil, false
	}
	leases, tenants = map[string]bool{}, map[string]bool{}
	if ds, err := a.s.M.store.Assignments(r.Context(), p.HostID); err == nil {
		for _, d := range ds {
			leases[LeaseName(d.TenantID, d.Workload)] = true
			tenants[d.TenantID] = true
		}
	}
	return leases, tenants, true
}

// ---- views (what the spec's schemas require beyond the store types) ----

type hostView struct {
	Host
	Status  string `json:"status"`
	DNSName string `json:"dnsName,omitempty"`
	Push    *struct {
		Port int    `json:"port,omitempty"`
		URL  string `json:"url,omitempty"`
	} `json:"push,omitempty"`
	Leases []Lease `json:"leases,omitempty"`

	Skew string `json:"skew,omitempty"` // versions.go

	// AuditHead: only in the RegisterHost answer (hostauditresume.go).
	AuditHead string `json:"auditHead,omitempty"`
}

func (a *v1api) hostView(r *http.Request, h Host) hostView {
	v := hostView{Host: h, Skew: a.s.M.hostSkew(h)}
	switch {
	case h.Pending():
		v.Status = "pending"
	case h.Live(a.s.M.now()):
		v.Status = "live"
	default:
		v.Status = "stale"
	}
	if h.NodeID != "" && a.s.Self.Tailnet != "" {
		v.DNSName = HostNodeName(h.ID) + "." + a.s.Self.Tailnet
	}
	if u := a.s.M.pushTarget(h.ID); u != "" || h.PushPort > 0 {
		v.Push = &struct {
			Port int    `json:"port,omitempty"`
			URL  string `json:"url,omitempty"`
		}{Port: h.PushPort}
		if strings.HasPrefix(u, "unix://") || strings.HasPrefix(u, "inproc://") {
			v.Push.URL = u
		}
	}
	if ls, err := a.s.M.HostLeases(r.Context(), h.ID); err == nil && len(ls) > 0 {
		v.Leases = ls
	}
	return v
}

type tombstone struct {
	ObjectID       string `json:"objectID"`
	TenantID       string `json:"tenantID"`
	Workload       string `json:"workload"`
	Namespace      string `json:"namespace"`
	ObjectName     string `json:"objectName"`
	KilledAt       int64  `json:"killedAt"`
	Reason         string `json:"reason,omitempty"`
	By             string `json:"by"`
	Cascade        bool   `json:"cascade"`
	Root           string `json:"root"`
	DeviceDeleted  bool   `json:"deviceDeleted"`
	DeviceDeleteAt int64  `json:"deviceDeleteAt,omitempty"`
}

func (a *v1api) tombstoneOf(o Object) tombstone {
	t := tombstone{ObjectID: o.ID, TenantID: o.TenantID, Workload: o.Workload, Namespace: o.Namespace, ObjectName: o.Name,
		KilledAt: o.KilledAt, Reason: o.KillReason, By: o.KilledBy, Cascade: o.KillCascade, Root: o.KillRoot}
	if t.KilledAt == 0 {
		t.KilledAt = o.UpdatedAt
	}
	if t.Root == "" {
		t.Root = o.ID
	}
	if t.By == "" {
		t.By = "mgmt"
	}
	if at, pending := a.s.M.pendingDeleteAt(o.ID); pending {
		t.DeviceDeleteAt = at
	} else {
		a.s.M.mu.Lock()
		kept := a.s.M.deviceKept[o.ID]
		a.s.M.mu.Unlock()
		t.DeviceDeleted = (o.StableNodeID != "" || o.NodeID != "") && !kept
	}
	return t
}

type objectView struct {
	Object
	Lineage   []string   `json:"lineage"`
	Tombstone *tombstone `json:"tombstone,omitempty"`
}

// objectView fills lineage (root → self) and the tombstone of a killed
// object. idx (optional) resolves parents from an already listed set.
func (a *v1api) objectView(r *http.Request, o Object, idx map[string]Object) objectView {
	v := objectView{Object: o, Lineage: []string{o.ID}}
	seen := map[string]bool{o.ID: true}
	for cur := o; cur.ParentID != "" && !seen[cur.ParentID]; {
		seen[cur.ParentID] = true
		p, ok := idx[cur.ParentID]
		if !ok {
			var err error
			if p, err = a.s.M.store.GetObject(r.Context(), cur.ParentID); err != nil {
				break
			}
		}
		v.Lineage = append([]string{p.ID}, v.Lineage...)
		cur = p
	}
	if o.Status == StatusKilled {
		t := a.tombstoneOf(o)
		v.Tombstone = &t
	}
	return v
}

// goneError is the 410 body: the typed error plus the kill record.
type goneError struct {
	Error
	Tombstone tombstone `json:"tombstone"`
}

func (a *v1api) writeGone(w http.ResponseWriter, r *http.Request, o Object) {
	e := *errf(410, "killed", "object %s was killed (%s)", o.ID, o.KillReason)
	e.TraceID = r.Header.Get("Tw-Trace-ID")
	if e.TraceID == "" {
		e.TraceID = traceID()
	}
	w.Header().Set("Tw-Error", "killed")
	writeJSON(w, 410, goneError{Error: e, Tombstone: a.tombstoneOf(o)})
}

// ---- operational ----

func (a *v1api) GetHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *v1api) WhoAmI(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Kind == "" {
		writeErr(w, r, errf(401, "no_identity", "unauthenticated"))
		return
	}
	kind := p.Kind
	if kind == "user" && p.NodeID != "" && len(p.Tags) > 0 {
		kind = "node"
	}
	self := a.s.Self
	self.Transport = "tsnet"
	if p.Kind == "local" {
		self.Transport = "local-dev"
	}
	who := WhoAmI{Kind: kind, Login: p.Login, NodeID: p.NodeID, Tags: p.Tags, HostID: p.HostID,
		Admin: a.s.allowed(p, roleAdmin), Actor: p.Actor(r), Mgmt: self,
		Caps: twCaps(p), Approver: p.Kind == "user" && isApprover(p), MCPTools: a.s.mcpToolNames(p)}
	a.s.whoamiScope(r.Context(), p, &who) // whoami_tenants.go
	writeJSON(w, 200, who)
}

func (a *v1api) GetStatus(w http.ResponseWriter, r *http.Request) {
	st, err := a.s.M.Status(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	transport := "tsnet"
	if principalOf(r.Context()).Kind == "local" {
		transport = "local-dev"
	}
	listeners := a.s.Listeners
	if listeners == nil {
		listeners = []string{}
	}
	writeJSON(w, 200, map[string]any{"ok": st.Clock.OK && st.Replicas.OK, "transport": transport, "listeners": listeners,
		"pendingDeviceDeletes": st.PendingDeletes, "hosts": map[string]int{"total": st.Hosts, "live": st.LiveHosts},
		"tenants": st.Tenants, "objects": st.Objects, "version": cmp.Or(a.s.Version, a.s.M.Version()), "clock": st.Clock, "replicas": st.Replicas,
		"versions": st.Versions})
}

func (a *v1api) CreateDeviceDeleteRun(w http.ResponseWriter, r *http.Request) {
	ids := a.s.M.pendingIDs()
	a.s.M.FlushDeletes()
	writeJSON(w, 201, map[string]any{"deleted": ids})
}

// ---- tenants ----

type tenantView struct {
	Tenant
	OBOPublicKey  string  `json:"oboPublicKey"`
	MintQueue     int     `json:"mintQueue"`
	EffectiveRate float64 `json:"effectiveRegistrationsPerMinute"`
	// WarmKeys: pooled preauthorized join keys in custody (warmcustody.go).
	WarmKeys int `json:"warmKeys"`
}

func (a *v1api) tenantView(r *http.Request, t Tenant) (tenantView, error) {
	m := a.s.M
	pub, err := m.OBOPublicKey(r.Context(), t.ID)
	if err != nil {
		return tenantView{}, err
	}
	return tenantView{Tenant: t, OBOPublicKey: pub, MintQueue: m.QueueDepth(t.ID), EffectiveRate: m.throttle(t).Rate(),
		WarmKeys: m.warmKeysHeld(r.Context(), t.ID)}, nil
}

func (a *v1api) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var req TenantRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	t, err := a.s.M.CreateTenant(r.Context(), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.tenantView(r, t)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	location(w, "/v1/tenants/"+t.ID)
	writeResource(w, r, 201, v)
}

func (a *v1api) ListTenants(w http.ResponseWriter, r *http.Request, params v1.ListTenantsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	_, mine, isHost := a.hostScope(r)
	scoped := a.s.tenantScoped(principalOf(r.Context()))
	if isHost || scoped {
		pg.Limit = 0 // filter in memory, then page
	}
	ts, err := a.s.M.store.ListTenants(r.Context(), pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if isHost {
		// Hosts see only the tenants with a deployment assigned to them.
		kept := make([]Tenant, 0, len(ts))
		for _, t := range ts {
			if mine[t.ID] {
				kept = append(kept, t)
			}
		}
		ts = kept
		if len(ts) > n+1 {
			ts = ts[:n+1]
		}
	}
	if p := principalOf(r.Context()); scoped {
		// Tenant admins see only the tenants their grants name (tenantscope.go).
		ts = slices.DeleteFunc(ts, func(t Tenant) bool { return !p.tenantAllows(t.ID, ActionRead) })
		if len(ts) > n+1 {
			ts = ts[:n+1]
		}
	}
	writeJSON(w, 200, mkPage(ts, n, func(t Tenant) PageKey { return PageKey{Created: t.CreatedAt, ID: t.ID} }))
}

func (a *v1api) GetTenant(w http.ResponseWriter, r *http.Request, id v1.TenantID, _ v1.GetTenantParams) {
	if _, mine, isHost := a.hostScope(r); isHost && !mine[id] {
		// M3: as if it did not exist (no confirmation of other tenants).
		writeErr(w, r, errf(404, "not_found", "host %s has no deployment of tenant %s", principalOf(r.Context()).HostID, id))
		return
	}
	t, err := a.s.M.store.GetTenant(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.tenantView(r, t)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, v)
}

func (a *v1api) UpdateTenant(w http.ResponseWriter, r *http.Request, id v1.TenantID, _ v1.UpdateTenantParams) {
	cur, err := a.s.M.store.GetTenant(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	curView, err := a.tenantView(r, cur)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := checkIfMatch(r, curView); err != nil {
		w.Header().Set("ETag", etagOf(curView))
		writeErr(w, r, err)
		return
	}
	var body map[string]json.RawMessage
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	var p TenantPatch
	p.By = principalOf(r.Context()).Actor(r)
	for k, raw := range body {
		null := string(raw) == "null"
		var sp *string
		var ip *int
		switch k {
		case "mintMode", "apiKeyFile", "oauthClientID", "oauthClientFile", "pool", "tier", "publicIngress", "ownerTag", "namePrefix":
			var s string
			if !null {
				if err := json.Unmarshal(raw, &s); err != nil {
					writeErr(w, r, errf(400, "bad_request", "%s must be a string", k))
					return
				}
			}
			sp = &s
		case "maxContainerVCPU":
			var f float64
			if null {
				writeErr(w, r, errf(400, "bad_request", "%s cannot be null", k))
				return
			}
			if err := json.Unmarshal(raw, &f); err != nil {
				writeErr(w, r, errf(400, "bad_request", "%s must be a number", k))
				return
			}
			p.MaxContainerVCPU = &f
			continue
		case QuotaMaxStoreBytes, QuotaMaxQueueBytes, QuotaMaxStores: // quota.go
			if err := p.quotaPatch(k, raw); err != nil {
				writeErr(w, r, err)
				return
			}
			continue
		case "registrationsPerMinute", "maxObjects", "maxContainers", "maxContainerMemMiB":
			var n int
			if null {
				writeErr(w, r, errf(400, "bad_request", "%s cannot be null", k))
				return
			}
			if err := json.Unmarshal(raw, &n); err != nil {
				writeErr(w, r, errf(400, "bad_request", "%s must be an integer", k))
				return
			}
			ip = &n
		case "wifClientID", "wifLifecycleClientID":
			writeErr(w, r, errSetRemovedMintMode(mintRemovedWIF))
			return
		case "tailnet", "controlURL", "tenantID", "createdAt", "updatedAt", "oboPublicKey":
			e := errf(400, "immutable_field", "%s cannot be changed; create a new tenant", k)
			e.Missing = map[string]any{"field": k}
			writeErr(w, r, e)
			return
		default:
			writeErr(w, r, errf(400, "bad_request", "unknown field %q", k))
			return
		}
		switch k {
		case "mintMode":
			if null {
				writeErr(w, r, errf(400, "bad_request", "mintMode cannot be null"))
				return
			}
			p.MintMode = sp
		case "apiKeyFile":
			p.APIKeyFile = sp
		case "oauthClientID":
			p.OAuthClientID = sp
		case "oauthClientFile":
			p.OAuthClientFile = sp
		case "pool":
			p.Pool = sp
		case "tier":
			p.Tier = sp
		case "publicIngress":
			p.PublicIngress = sp
		case "ownerTag":
			p.OwnerTag = sp
		case "namePrefix":
			p.NamePrefix = sp
		case "registrationsPerMinute":
			p.RegistrationsPerMinute = ip
		case "maxObjects":
			p.MaxObjects = ip
		case "maxContainers":
			p.MaxContainers = ip
		case "maxContainerMemMiB":
			p.MaxContainerMemMiB = ip
		}
	}
	t, err := a.s.M.UpdateTenant(r.Context(), id, p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.tenantView(r, t)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, v)
}

func (a *v1api) DeleteTenant(w http.ResponseWriter, r *http.Request, id v1.TenantID, params v1.DeleteTenantParams) {
	cur, err := a.s.M.store.GetTenant(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if curView, err := a.tenantView(r, cur); err == nil {
		if err := checkIfMatch(r, curView); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	res, err := a.s.M.DeleteTenant(r.Context(), id, boolOf(params.Force), principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(res.Killed) == 0 && len(res.DeploymentsStopped) == 0 {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 202, res)
}

func (a *v1api) PurgeTenantObjects(w http.ResponseWriter, r *http.Request, id v1.TenantID, params v1.PurgeTenantObjectsParams) {
	var states []string
	if params.State != nil {
		for _, s := range *params.State {
			states = append(states, string(s))
		}
	}
	res, err := a.s.M.Purge(r.Context(), id, states...)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

// ---- hosts ----

func (a *v1api) CreateHost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID   string   `json:"hostID"`
		TenantID string   `json:"tenantID"`
		Pool     string   `json:"pool"`
		Tiers    []string `json:"tiers"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	hj, created, err := a.s.M.CreateHost(r.Context(), HostCreate{HostID: req.HostID, TenantID: req.TenantID, Pool: req.Pool,
		Tiers: req.Tiers, By: principalOf(r.Context()).Actor(r)})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	out := struct {
		Host               hostView `json:"host"`
		AuthKey            string   `json:"authKey"`
		RegistrationSecret string   `json:"registrationSecret"`
		ExpiresAt          int64    `json:"expiresAt"`
		Tags               []string `json:"tags"`
		Hostname           string   `json:"hostname"`
		ControlURL         string   `json:"controlURL,omitempty"`
		Tailnet            string   `json:"tailnet,omitempty"`
		KeyTagsOnly        bool     `json:"keyTagsOnly,omitempty"`
		MgmtURL            string   `json:"mgmtURL,omitempty"`
	}{a.hostView(r, hj.Host), hj.AuthKey, hj.RegistrationSecret, hj.ExpiresAt, hj.Tags, hj.Hostname, hj.ControlURL, hj.Tailnet,
		hj.KeyTagsOnly, a.hostJoinMgmtURL()}
	if created {
		location(w, "/v1/hosts/"+hj.Host.ID)
		writeJSON(w, 201, out)
		return
	}
	writeJSON(w, 200, out)
}

func (a *v1api) ListHosts(w http.ResponseWriter, r *http.Request, params v1.ListHostsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if params.Live != nil {
		pg.Limit = 0 // filter in memory, then page
	}
	hs, err := a.s.M.store.ListHosts(r.Context(), pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views := make([]hostView, 0, len(hs))
	for _, h := range hs {
		if params.Live != nil && h.Live(a.s.M.now()) != *params.Live {
			continue
		}
		views = append(views, a.hostView(r, h))
	}
	if params.Live != nil && len(views) > n+1 {
		views = views[:n+1]
	}
	writeJSON(w, 200, mkPage(views, n, func(h hostView) PageKey { return PageKey{Created: h.CreatedAt, ID: h.ID} }))
}

func (a *v1api) GetHost(w http.ResponseWriter, r *http.Request, id v1.HostID, _ v1.GetHostParams) {
	h, err := a.s.M.store.GetHost(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, a.hostView(r, h))
}

func (a *v1api) RegisterHost(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	var body struct {
		RegisterRequest
		Push *struct {
			Port int    `json:"port"`
			URL  string `json:"url"`
		} `json:"push"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	req := body.RegisterRequest
	if body.Push != nil { // the spec's HostRegistration.push {url | port}
		req.PushURL, req.PushPort = body.Push.URL, body.Push.Port
	}
	req.HostID = id
	p := principalOf(r.Context())
	if p.Kind == "host" && p.HostID != "" && p.HostID != id {
		writeErr(w, r, errf(403, "host_identity_mismatch", "this node is registered as host %s", p.HostID))
		return
	}
	nodeAddr := ""
	if p.Kind == "host" {
		if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			nodeAddr = h
		}
	}
	h, created, err := a.s.M.RegisterHost(r.Context(), req, p.NodeID, nodeAddr)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	v := a.hostView(r, h)
	et := etagOf(v) // the resource's ETag, as GET answers it: auditHead is not part of the host record
	if v.AuditHead, err = a.s.M.hostAuditHead(r.Context(), h.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("ETag", et)
	writeJSON(w, status, v)
}

func (a *v1api) DeleteHost(w http.ResponseWriter, r *http.Request, id v1.HostID, params v1.DeleteHostParams) {
	cur, err := a.s.M.store.GetHost(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := checkIfMatch(r, a.hostView(r, cur)); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.DeleteHost(r.Context(), id, boolOf(params.Drain), principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(res.Killed) == 0 && len(res.LeasesReleased) == 0 {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 202, res)
}

func (a *v1api) ReportHostStatus(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	var req HostReport
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	resp, err := a.s.M.ReportHostStatus(r.Context(), id, req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, resp)
}

func (a *v1api) ReportHostPaths(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	var req struct {
		Tenants []PathReport `json:"tenants"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := a.s.M.ReportHostPaths(r.Context(), id, req.Tenants)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int{"stored": n})
}

type assignmentView struct {
	Assignment
	CreatedAt int64 `json:"createdAt"`
}

func (a *v1api) ListHostAssignments(w http.ResponseWriter, r *http.Request, id v1.HostID, params v1.ListHostAssignmentsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := a.s.M.store.GetHost(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	as, err := a.s.M.Assignments(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views := make([]assignmentView, 0, len(as))
	for _, x := range as {
		d, _ := a.s.M.store.GetDeployment(r.Context(), x.DeploymentID)
		k := PageKey{Created: d.CreatedAt, ID: d.ID}
		if pg.After != nil && !(k.Created > pg.After.Created || (k.Created == pg.After.Created && k.ID > pg.After.ID)) {
			continue
		}
		views = append(views, assignmentView{x, d.CreatedAt})
	}
	if len(views) > n+1 {
		views = views[:n+1]
	}
	writeJSON(w, 200, mkPage(views, n, func(v assignmentView) PageKey { return PageKey{Created: v.CreatedAt, ID: v.DeploymentID} }))
}

// ---- deployments ----

type deploymentView struct {
	DeploymentView
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

func (a *v1api) deploymentView(r *http.Request, id string) (deploymentView, error) {
	v, err := a.s.M.GetDeployment(r.Context(), id)
	if err != nil {
		return deploymentView{}, err
	}
	if v.Endpoint.Status == "" {
		v.Endpoint.Status = StatusPending
	}
	if v.Endpoint.Node == "" {
		d, _ := a.s.M.store.GetDeployment(r.Context(), id)
		t, _ := a.s.M.store.GetTenant(r.Context(), d.TenantID)
		v.Endpoint.Node = t.Name(d.Workload) + "-" + d.HostID
		v.Endpoint.MagicDNS = v.Endpoint.Node + "." + t.Tailnet
	}
	if v.Namespaces == nil {
		v.Namespaces = []ClassConfig{}
	}
	return deploymentView{DeploymentView: v}, nil
}

func (a *v1api) CreateDeployment(w http.ResponseWriter, r *http.Request, params v1.CreateDeploymentParams) {
	var req DeployRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.tenantOK(r, req.TenantID, true); err != nil {
		writeErr(w, r, err)
		return
	}
	req.By = principalOf(r.Context()).Actor(r)
	if boolOf(params.DryRun) {
		res, err := a.s.M.PreflightBundle(r.Context(), req)
		if err == nil {
			err = a.s.M.checkReplicaName(r.Context(), req) // the deploy would refuse the name (workload_replicas.go, #348)
		}
		if err == nil {
			err = a.s.M.checkCompatFlags(r.Context(), req) // the deploy would refuse the flags (compatflags.go, #337)
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, res)
		return
	}
	resp, err := a.s.M.Deploy(r.Context(), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.deploymentView(r, resp.DeploymentID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	location(w, "/v1/deployments/"+resp.DeploymentID)
	writeJSON(w, 202, v)
}

func (a *v1api) ListDeployments(w http.ResponseWriter, r *http.Request, params v1.ListDeploymentsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	tenant, mine, err := a.tenantListScope(r, params.Tenant) // tenantscope.go
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if mine != nil {
		pg.Limit = 0 // filter in memory, then page
	}
	ds, err := a.s.M.store.ListDeployments(r.Context(), DeploymentFilter{TenantID: tenant, Workload: str(params.Workload),
		HostID: str(params.Host), Latest: boolOf(params.Latest)}, pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ds = keepTenants(ds, mine, n, func(d Deployment) string { return d.TenantID })
	views := make([]deploymentView, 0, len(ds))
	for _, d := range ds {
		if v, err := a.deploymentView(r, d.ID); err == nil {
			views = append(views, v)
		}
	}
	writeJSON(w, 200, mkPage(views, n, func(v deploymentView) PageKey { return PageKey{Created: v.CreatedAt, ID: v.DeploymentID} }))
}

func (a *v1api) GetDeployment(w http.ResponseWriter, r *http.Request, id v1.DeploymentID, _ v1.GetDeploymentParams) {
	v, err := a.deploymentView(r, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if p := principalOf(r.Context()); p.Kind == "host" && v.Host != p.HostID {
		writeErr(w, r, errf(403, "forbidden", "deployment %s is not assigned to host %s", id, p.HostID))
		return
	}
	if err := a.tenantOK(r, v.TenantID, false); err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, v)
}

func (a *v1api) DeleteDeployment(w http.ResponseWriter, r *http.Request, id v1.DeploymentID, params v1.DeleteDeploymentParams) {
	cur, err := a.deploymentView(r, id)
	if err == nil {
		err = a.tenantOK(r, cur.TenantID, false)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := checkIfMatch(r, cur); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.Undeploy(r.Context(), id, str(params.Reason), principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 202, res)
}

func (a *v1api) GetDeploymentBundle(w http.ResponseWriter, r *http.Request, id v1.DeploymentID) {
	d, err := a.s.M.store.GetDeployment(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if p := principalOf(r.Context()); p.Kind == "host" && d.HostID != p.HostID {
		writeErr(w, r, errf(403, "forbidden", "deployment %s is not assigned to host %s", d.ID, p.HostID))
		return
	}
	if err := a.tenantOK(r, d.TenantID, false); err != nil {
		writeErr(w, r, err)
		return
	}
	sum := sha256.Sum256(d.Bundle)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Write(d.Bundle)
}

// ---- objects ----

func (a *v1api) CreateObject(w http.ResponseWriter, r *http.Request) {
	var req CreateObjectRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.TraceID == "" {
		req.TraceID = r.Header.Get("Tw-Trace-ID")
	}
	if p := principalOf(r.Context()); p.Kind == "host" {
	}
	// A killed identity answers 410 with its tombstone.
	if req.Namespace != "" && req.ObjectName != "" {
		wl := req.Workload
		if req.Namespace == WorkloadNamespace || req.Namespace == PublicNamespace {
			wl = req.ObjectName
		}
		if wl != "" {
			if o, err := a.s.M.store.GetObjectByName(r.Context(), req.TenantID, wl, req.Namespace, req.ObjectName); err == nil && o.Status == StatusKilled {
				if err := a.requireLease(r, o.Lease); err != nil {
					writeErr(w, r, err)
					return
				}
				a.writeGone(w, r, o)
				return
			}
		}
	}
	resp, err := a.s.M.CreateObject(r.Context(), req.WithHost(hostOf(r)))
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == "killed" {
			if objs, lerr := a.s.M.store.ListObjects(r.Context(), ObjectFilter{TenantID: req.TenantID, Namespace: req.Namespace, Status: []string{StatusKilled}}); lerr == nil {
				for _, o := range objs {
					if o.Name == req.ObjectName && (req.Workload == "" || o.Workload == req.Workload) {
						a.writeGone(w, r, o)
						return
					}
				}
			}
		}
		writeErr(w, r, err)
		return
	}
	o, err := a.s.M.GetObject(r.Context(), resp.ObjectID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if resp.Created {
		// Accepted: the mint is queued. The body is the resource as created
		// (the mint worker may already have moved it on; poll Location).
		if o.Status == StatusMinted {
			o.Status = StatusPending
		}
		location(w, "/v1/objects/"+o.ID)
		writeJSON(w, 202, a.objectView(r, o, nil))
		return
	}
	writeResource(w, r, 200, a.objectView(r, o, nil))
}

func (a *v1api) ListObjects(w http.ResponseWriter, r *http.Request, params v1.ListObjectsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	tenant, mine, err := a.tenantListScope(r, params.Tenant) // tenantscope.go
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f := ObjectFilter{TenantID: tenant, Workload: str(params.Workload), Namespace: str(params.Namespace),
		ParentID: str(params.Parent), RootID: str(params.Root), Page: pg,
		Tag: str(params.Tag), ActingFor: str(params.Human)}
	if params.State != nil {
		for _, s := range *params.State {
			f.Status = append(f.Status, string(s))
		}
	}
	leases, _, isHost := a.hostScope(r)
	if isHost || mine != nil {
		f.Page.Limit = 0 // filter in memory, then page
	}
	objs, err := a.s.M.store.ListObjects(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	objs = keepTenants(objs, mine, n, func(o Object) string { return o.TenantID })
	if isHost {
		// M3: a host lists only objects whose lease it holds.
		kept := make([]Object, 0, len(objs))
		for _, o := range objs {
			if leases[o.Lease] {
				kept = append(kept, o)
			}
		}
		objs = kept
		if len(objs) > n+1 {
			objs = objs[:n+1]
		}
	}
	idx := map[string]Object{}
	for _, o := range objs {
		idx[o.ID] = o
	}
	views := make([]objectView, 0, len(objs))
	for _, o := range objs {
		v := a.objectView(r, o, idx)
		for _, k := range objs {
			if k.ParentID == o.ID {
				v.Children = append(v.Children, k.ID)
			}
		}
		views = append(views, v)
	}
	writeJSON(w, 200, mkPage(views, n, func(v objectView) PageKey { return PageKey{Created: v.CreatedAt, ID: v.ID} }))
}

func (a *v1api) GetObject(w http.ResponseWriter, r *http.Request, id v1.ObjectID, _ v1.GetObjectParams) {
	o, err := a.s.M.GetObject(r.Context(), id)
	if err == nil {
		err = a.tenantOK(r, o.TenantID, false)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.requireLease(r, o.Lease); err != nil {
		writeErr(w, r, err)
		return
	}
	if o.Status == StatusKilled {
		a.writeGone(w, r, o)
		return
	}
	writeResource(w, r, 200, a.objectView(r, o, nil))
}

func (a *v1api) UpdateObject(w http.ResponseWriter, r *http.Request, id v1.ObjectID, _ v1.UpdateObjectParams) {
	cur, err := a.s.M.GetObject(r.Context(), id)
	if err == nil {
		err = a.tenantOK(r, cur.TenantID, false)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.requireLease(r, cur.Lease); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := checkIfMatch(r, a.objectView(r, cur, nil)); err != nil {
		writeErr(w, r, err)
		return
	}
	var body map[string]json.RawMessage
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	p := principalOf(r.Context())
	_, ready := body["status"]
	_, ttl := body["ttl"]
	if cur.Status == StatusKilled && !ready {
		// A ready report still goes through Ready: it deletes the device
		// that joined after the kill, then answers 410.
		a.writeGone(w, r, cur)
		return
	}
	for k := range body {
		switch k {
		case "status", "nodeID", "stableNodeID", "magicDNS", "epoch", "ttl":
		default:
			e := errf(400, "immutable_field", "%s cannot be changed", k)
			e.Missing = map[string]any{"field": k}
			writeErr(w, r, e)
			return
		}
	}
	switch {
	case ready && ttl:
		writeErr(w, r, errf(400, "immutable_field", "a patch is either the host's {status: ready, …} or the admin's {ttl}"))
		return
	case ready:
		if p.Kind == "user" {
			writeErr(w, r, errf(400, "immutable_field", "only the lease-holding host reports status"))
			return
		}
		var req struct {
			Status string `json:"status"`
			ReadyRequest
		}
		raw, _ := json.Marshal(body)
		if err := json.Unmarshal(raw, &req); err != nil || req.Status != StatusReady {
			writeErr(w, r, errf(400, "bad_request", "status must be \"ready\" with nodeID, stableNodeID, magicDNS and epoch"))
			return
		}
		o, err := a.s.M.Ready(r.Context(), id, hostOf(r), req.ReadyRequest)
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Code == "killed" {
				if k, gerr := a.s.M.store.GetObject(r.Context(), id); gerr == nil {
					a.writeGone(w, r, k)
					return
				}
			}
			writeErr(w, r, err)
			return
		}
		writeResource(w, r, 200, a.objectView(r, o, nil))
	case ttl:
		if p.Kind == "host" {
			writeErr(w, r, errf(400, "immutable_field", "hosts cannot change ttl"))
			return
		}
		var secs *int64
		if string(body["ttl"]) != "null" {
			var n int64
			if err := json.Unmarshal(body["ttl"], &n); err != nil || n <= 0 {
				writeErr(w, r, errf(400, "bad_request", "ttl must be a positive number of seconds or null"))
				return
			}
			secs = &n
		}
		o, err := a.s.M.SetObjectTTL(r.Context(), id, secs, p.Actor(r))
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeResource(w, r, 200, a.objectView(r, o, nil))
	default:
		writeErr(w, r, errf(400, "bad_request", "empty patch"))
	}
}

func (a *v1api) DeleteObject(w http.ResponseWriter, r *http.Request, id v1.ObjectID, params v1.DeleteObjectParams) {
	cur, err := a.s.M.GetObject(r.Context(), id)
	if err == nil {
		err = a.tenantOK(r, cur.TenantID, false)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if cur.Status == StatusKilled {
		a.writeGone(w, r, cur)
		return
	}
	if err := checkIfMatch(r, a.objectView(r, cur, nil)); err != nil {
		writeErr(w, r, err)
		return
	}
	p := principalOf(r.Context())
	if err := a.requireLease(r, cur.Lease); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.Kill(r.Context(), id, KillRequest{Cascade: boolOf(params.Cascade), Reason: str(params.Reason), By: p.Actor(r)})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 202, res)
}

func (a *v1api) CreateObjectCredential(w http.ResponseWriter, r *http.Request, id v1.ObjectID, params v1.CreateObjectCredentialParams) {
	if params.Epoch <= 0 {
		writeErr(w, r, errf(400, "bad_request", "epoch query parameter must be positive"))
		return
	}
	wait := 10 * time.Second
	if params.Wait != nil && *params.Wait >= 0 && *params.Wait <= 30 {
		wait = time.Duration(*params.Wait) * time.Second
	}
	if cur, err := a.s.M.store.GetObject(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	} else if err := a.requireLease(r, cur.Lease); err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	host := hostOf(r)
	reminted := false
	if params.Reason != nil && *params.Reason == "rejoin_rejected" {
		if _, err := a.s.M.Remint(r.Context(), id, host, RemintRequest{Epoch: params.Epoch, Reason: "saved node state rejected by control (rejoin_rejected)"}); err != nil {
			var e *Error
			if errors.As(err, &e) && e.Code == "killed" {
				if k, gerr := a.s.M.store.GetObject(r.Context(), id); gerr == nil {
					a.writeGone(w, r, k)
					return
				}
			}
			writeErr(w, r, err)
			return
		}
		reminted = true
	}
	c, err := a.s.M.Credential(r.Context(), id, host, params.Epoch, wait)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == "killed" {
			if k, gerr := a.s.M.store.GetObject(r.Context(), id); gerr == nil {
				a.writeGone(w, r, k)
				return
			}
		}
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, struct {
		Credential
		Reminted bool `json:"reminted,omitempty"`
	}{c, reminted})
}

func (a *v1api) GetObjectCertificate(w http.ResponseWriter, r *http.Request, id v1.ObjectID) {
	o, err := a.s.M.store.GetObject(r.Context(), id)
	setObjectCertDeprecation(w, o, err) // deprecated_routes.go
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if o.Status == StatusKilled {
		a.writeGone(w, r, o)
		return
	}
	if o.StableNodeID == "" {
		writeErr(w, r, errf(409, "not_ready", "object %s has no node yet (%s)", id, o.Status))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	nc, err := a.s.M.NodeCert(r.Context(), o.TenantID, o.StableNodeID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, nc)
}

func (a *v1api) GetCertificate(w http.ResponseWriter, r *http.Request, stableNodeID string, params v1.GetCertificateParams) {
	w.Header().Set("Cache-Control", "no-store")
	nc, err := a.s.M.NodeCert(r.Context(), params.Tenant, stableNodeID)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == "killed" {
			if objs, lerr := a.s.M.store.ListObjects(r.Context(), ObjectFilter{TenantID: params.Tenant, StableNodeID: stableNodeID}); lerr == nil && len(objs) > 0 {
				a.writeGone(w, r, objs[len(objs)-1])
				return
			}
		}
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, nc)
}

// ---- audit ----

func (a *v1api) AppendAudit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	host := hostOf(r)
	if host == "" {
		writeErr(w, r, errf(400, "bad_request", "audit records need a host (Tw-Host-ID header on the local-dev socket)"))
		return
	}
	n, st, err := a.s.M.IngestAudit(r.Context(), host, req.Records)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"accepted": n, "chain": st})
}

func (a *v1api) ListAudit(w http.ResponseWriter, r *http.Request, params v1.ListAuditParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	tenant, mine, err := a.tenantListScope(r, params.Tenant) // tenantscope.go
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f := AuditFilter{ObjectID: str(params.Object), ActingFor: str(params.Human), TraceID: str(params.Trace), TenantID: tenant,
		Chain: str(params.Chain), Since: normTS(str(params.Since)), Until: normTS(str(params.Until)), Page: pg,
		Workload: str(params.Workload)}
	f.Desc = params.Order != nil && *params.Order == v1.Desc
	if params.Kind != nil {
		for _, k := range *params.Kind {
			f.Kinds = append(f.Kinds, string(k))
		}
	}
	var recs []AuditRecord
	var chains []ChainStatus
	if mine != nil {
		recs, chains, err = a.s.M.auditForTenants(r.Context(), f, mine, n)
	} else {
		recs, chains, err = a.s.M.QueryAudit(r.Context(), f)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	pageRecs := mkPage(recs, n, func(x AuditRecord) PageKey { return PageKey{TS: x.TS, Chain: x.Chain, Seq: x.Seq} })
	ok := true
	for _, c := range chains {
		ok = ok && c.OK
	}
	if chains == nil || a.s.tenantScoped(principalOf(r.Context())) {
		chains = []ChainStatus{} // host chains are the operator's (tenantscope.go)
	}
	writeJSON(w, 200, struct {
		page[AuditRecord]
		Chains   []ChainStatus `json:"chains"`
		Verified bool          `json:"verified"`
	}{pageRecs, chains, ok})
}

// normTS accepts RFC 3339 or unix ms and returns RFC 3339 (the stored form).
func normTS(s string) string {
	if s == "" {
		return ""
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	}
	return s
}

func (a *v1api) GetAuditVerification(w http.ResponseWriter, r *http.Request) {
	chains, ok, err := a.s.M.VerifyAll(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": ok, "chains": chains, "verifiedAt": a.s.M.now().UnixMilli()})
}

// ---- volumes ----

func (a *v1api) CreateVolumeKeyGrant(w http.ResponseWriter, r *http.Request, id v1.VolumeID, params v1.CreateVolumeKeyGrantParams) {
	tenantID, workload, ok := strings.Cut(id, ":")
	if !ok || tenantID == "" || workload == "" {
		writeErr(w, r, errf(400, "bad_request", "volume ID must be <tenantID>:<workload>"))
		return
	}
	if params.Epoch <= 0 {
		writeErr(w, r, errf(400, "bad_request", "epoch query parameter must be positive"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	host := hostOf(r)
	key, err := a.s.M.VolumeKey(r.Context(), tenantID, workload, host, params.Epoch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, struct {
		VolumeID  string `json:"volumeID"`
		TenantID  string `json:"tenantID"`
		Workload  string `json:"workload"`
		Epoch     int64  `json:"epoch"`
		Key       []byte `json:"key"`
		GrantedAt int64  `json:"grantedAt"`
	}{id, tenantID, workload, params.Epoch, key, a.s.M.now().UnixMilli()})
}

// ---- container reservations (ADR-032 D9) ----

func (a *v1api) CreateContainerReservation(w http.ResponseWriter, r *http.Request) {
	var req ReserveRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	res, created, err := a.s.M.ReserveContainer(r.Context(), hostOf(r), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if created {
		location(w, "/v1/container-reservations/"+res.ID)
		writeJSON(w, 201, res)
		return
	}
	writeJSON(w, 200, res)
}

func (a *v1api) ListContainerReservations(w http.ResponseWriter, r *http.Request, params v1.ListContainerReservationsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f := ContainerFilter{TenantID: str(params.Tenant), HostID: str(params.Host)}
	if p := principalOf(r.Context()); p.Kind == "host" || r.Header.Get("Tw-Host-ID") != "" {
		f.HostID = hostOf(r) // a host sees its own reservations only
	}
	rs, err := a.s.M.store.ListContainerReservations(r.Context(), f, pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, mkPage(rs, n, func(c ContainerReservation) PageKey { return PageKey{Created: c.CreatedAt, ID: c.ID} }))
}

func (a *v1api) DeleteContainerReservation(w http.ResponseWriter, r *http.Request, id v1.ReservationID) {
	p := principalOf(r.Context())
	host := ""
	if p.Kind == "host" || p.Kind == "local" {
		host = hostOf(r) // "" for a local admin (tw CLI): any host's
	}
	if err := a.s.M.ReleaseContainer(r.Context(), id, host, p.Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

// ---- role middleware ----

// roleAllowed decides whether principal p may call an operation whose
// x-tw-role is role ("admin", "host", "admin|host", "any", "public").
func (s *Server) roleAllowed(p Principal, role string) bool {
	if role == "public" {
		return true
	}
	if p.Kind == "" {
		return false
	}
	if role == "any" || p.Kind == "local" {
		return true
	}
	roles := 0
	for _, part := range strings.Split(role, "|") {
		switch strings.TrimSpace(part) {
		case "admin":
			roles |= roleAdmin
		case "host":
			roles |= roleHost
		case "approver":
			roles |= roleApprover
		}
	}
	return s.allowed(p, roles)
}

// hostBodyLimit caps request bodies from the host role (L4); POST /v1/audit
// is exempt (readJSON's 64 MiB applies there, as to admin bundle uploads).
const hostBodyLimit = 1 << 20

// roleGate gates every generated route by the spec's x-tw-role and binds
// tsnet hosts to their own /v1/hosts/{id} records. It runs before the
// generated parameter binding (mux.Handler resolves the pattern without
// serving), so a malformed request from the wrong role is a 403, not a 400.
func (s *Server) roleGate(roles map[string]string, mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		_, pattern := mux.Handler(r)
		role, known := roles[pattern]
		if !known {
			if p.Kind == "" {
				writeErr(w, r, errf(401, "no_identity", "unauthenticated"))
				return
			}
			writeErr(w, r, errf(404, "not_found", "%s %s is not a /v1 route (see api/openapi.yaml)", r.Method, r.URL.Path))
			return
		}
		if !s.roleAllowed(p, role) {
			if !s.tenantScoped(p) {
				if s.holdsTenantAdminCap(p) {
					writeErr(w, r, noValidTenantGrant(p)) // tenantscope.go
					return
				}
				writeErr(w, r, errf(403, "forbidden", "caller (%s) may not call %s %s", p.Kind, r.Method, r.URL.Path))
				return
			}
			var ok bool
			if r, ok = s.tenantGate(w, r, p, role, pattern); !ok { // tenantscope.go
				return
			}
		}
		if p.Kind == "host" && r.Body != nil && pattern != "POST /v1/audit" {
			// L4: host-role bodies are small (registration, heartbeat, object
			// create, ready) except audit batches, which keep readJSON's limit,
			// and store journal appends (stores.go), which have their own.
			limit := int64(hostBodyLimit)
			if pattern == storeAppendPattern {
				limit = storeAppendBodyLimit
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		if p.Kind == "host" && p.HostID != "" && strings.HasPrefix(pattern[strings.Index(pattern, " ")+1:], "/v1/hosts/{id}") {
			// /v1/hosts/{id}[/...]: the id is the third path element.
			if parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4); len(parts) >= 3 && parts[2] != p.HostID {
				writeErr(w, r, errf(403, "forbidden", "host %s may not act as host %s", p.HostID, parts[2]))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// Handler returns the /v1 API mux: generated routing + parameter parsing
// over v1api, with the role gate from the spec.
func (s *Server) Handler() http.Handler {
	roles, err := v1.OperationRoles()
	if err != nil {
		panic("mgmt: embedded spec: " + err.Error())
	}
	mux := http.NewServeMux()
	v1.HandlerWithOptions(&v1api{s: s}, v1.StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErr(w, r, errf(400, "bad_request", "%v", err))
		},
	})
	// Anything else (there is no /v0) is a typed 404 from the gate.
	gate := s.roleGate(roles, mux)
	// The OCI distribution API (/v2/, ADR-032 D3) is an external spec:
	// api/openapi.yaml documents its routes and roles (tag "registry",
	// excluded from code generation); the registry enforces them.
	reg := s.M.Registry()
	mcp := s.mcpHandler(gate) // mcp.go: tailworks' MCP server, tools over the /v1 handlers
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == MCPPath {
			mcp.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v2" || strings.HasPrefix(r.URL.Path, "/v2/") {
			if reg == nil {
				writeErr(w, r, errf(404, "registry_unavailable", "this tw-mgmt serves no registry (start it with -registry-dir)"))
				return
			}
			p := principalOf(r.Context())
			if p.Kind == "" {
				writeErr(w, r, errf(401, "no_identity", "unauthenticated"))
				return
			}
			if p.Kind == "user" && !s.allowed(p, roleAdmin) {
				// Approvers and MCP-only callers never reach the registry;
				// tenant admins reach their own tenants' namespaces.
				if !s.tenantScoped(p) {
					writeErr(w, r, errf(403, "forbidden", "caller (%s) may not use the registry", p.Kind))
					return
				}
				r = withRegistryTenantScope(r) // tenantscope_registry.go
			}
			reg.ServeHTTP(w, r)
			return
		}
		gate.ServeHTTP(w, r)
	})
	// The admin UI (mgmt/ui) rides on the same principal; browserGuard
	// refuses cross-site browser mutations (CSRF) for the UI and the API.
	return versionHeader(s.M.Version(), browserGuard(s.Hosts, s.mountUI(api, embeddedUI(api))))
}

// mountUI dispatches the admin UI (mgmt/ui) next to the API: /ui/* goes to
// the UI once the caller is admitted (the local operator or an admin user;
// never a host), GET / from a browser (Accept: text/html) is redirected to
// /ui/, and everything else is the API. The UI itself is a plain /v1
// client (embeddedUI, uiclient.go) and also checks /v1/whoami, so this
// gate is a second layer; it sees exactly what the principal may see.
func (s *Server) mountUI(api, uiH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/"):
			p := principalOf(r.Context())
			if p.Kind == "" {
				writeErr(w, r, errf(401, "no_identity", "unauthenticated"))
				return
			}
			// An approver who is not an admin gets the approvals page only.
			approverPage := isApprover(p) && (r.URL.Path == "/ui/approvals" || r.URL.Path == "/ui/approvals/decide" || strings.HasPrefix(r.URL.Path, "/ui/static/"))
			if p.Kind == "host" || !s.allowed(p, roleAdmin) && !s.tenantScoped(p) && !approverPage { // tenant admins: tenantscope.go
				writeErr(w, r, errf(403, "forbidden", "caller (%s) may not use the admin UI", p.Kind))
				return
			}
			uiH.ServeHTTP(w, r)
		case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Contains(r.Header.Get("Accept"), "text/html"):
			http.Redirect(w, r, "/ui/", http.StatusFound)
		default:
			api.ServeHTTP(w, r)
		}
	})
}

// pendingIDs lists objects whose device delete is scheduled.
func (m *Mgmt) pendingIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.pending))
	for id := range m.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// pendingDeleteAt reports when an object's scheduled device delete runs.
func (m *Mgmt) pendingDeleteAt(id string) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return 0, false
	}
	return p.at, true
}

var _ = fmt.Sprintf

// ---- public ingress (ADR-029) ----

func (a *v1api) GetWorkloadPublic(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName) {
	wp, err := a.s.M.GetWorkloadPublic(r.Context(), id, workload)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, wp)
}

func (a *v1api) UpdateWorkloadPublic(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, _ v1.UpdateWorkloadPublicParams) {
	cur, err := a.s.M.GetWorkloadPublic(r.Context(), id, workload)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := checkIfMatch(r, cur); err != nil {
		w.Header().Set("ETag", etagOf(cur))
		writeErr(w, r, err)
		return
	}
	var body map[string]json.RawMessage
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	var exposed *bool
	for k, raw := range body {
		if k != "exposed" {
			writeErr(w, r, errf(400, "bad_request", "unknown field %q (only \"exposed\" can be changed)", k))
			return
		}
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			writeErr(w, r, errf(400, "bad_request", "exposed must be a boolean"))
			return
		}
		exposed = &b
	}
	if exposed == nil {
		writeErr(w, r, errf(400, "bad_request", "exposed is required"))
		return
	}
	wp, err := a.s.M.SetWorkloadExposed(r.Context(), id, workload, *exposed, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResource(w, r, 200, wp)
}
