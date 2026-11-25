package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/approvecap"
	"github.com/rosskukulinski/tailworks/netd"
)

// Approvals by tailnet identity (ADR-040, docs/approvals.md).
//
// A Workflow waits for an approval with step.waitForEvent({type:
// "approval" | "approval-*"}) (the Agents SDK's waitForApproval does). The
// runtime reports the wait here (platform TailnetApi "approval"); hostd
// lists it and is the only path that can deliver the decision:
//
//   - on the workload's (or an object's) own node: /_tw/approvals[...]
//     (ingress below). The approver is the request's WireGuard identity as
//     netd stamped it, and its grants are that caller's github.com/rosskukulinski/tailworks/cap/approve
//     toward the node;
//   - from mgmt (tw approve, the admin UI): POST /v1/approvals/decide on
//     the push API. mgmt sends the approver's WhoIs identity and their
//     github.com/rosskukulinski/tailworks/cap/approve toward mgmt; hostd checks them the same way.
//
// hostd authorises (authorizeApproval), sends the event through the
// platform (cf.tw kind "approval", which only hostd can set; the payload's
// "approver" is the verified identity, whatever the request said), and
// audits every request for approval, decision, refusal and timeout on the
// host chain.

// ApproveCap is the app capability that lets a tailnet identity decide
// approvals (internal/approvecap: the grant shape and its rules).
const ApproveCap = approvecap.Cap

// Approval statuses.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
	ApprovalTimedOut = "timeout"
)

// Audit kinds (host chain).
const (
	kindApprovalRequested = "approval_requested"
	kindApprovalDecision  = "approval_decision"
	kindApprovalDenied    = "approval_denied"
	kindApprovalTimeout   = "approval_timeout"
)

// maxDecidedApprovals bounds the decided records a host keeps (the audit
// log is the record; this is the list's history).
const maxDecidedApprovals = 1000

// Approver is the verified identity that decided an approval: netd's
// caller (WireGuard) or mgmt's WhoIs of the CLI / admin UI user.
type Approver struct {
	User   string   `json:"user,omitempty"` // login (untagged nodes)
	Node   string   `json:"node"`           // MagicDNS name
	NodeID string   `json:"nodeId"`         // stable node ID
	Tags   []string `json:"tags"`
	Via    string   `json:"via"` // "tailnet" (the node itself) | "mgmt" (tw approve, admin UI)
}

func (a Approver) principal() string {
	if a.User != "" {
		return a.User
	}
	if a.Node != "" {
		return strings.TrimSuffix(a.Node, ".")
	}
	return a.NodeID
}

// Approval is one approval wait of one Workflow instance.
type Approval struct {
	TenantID    string             `json:"tenantID"`
	Workload    string             `json:"workload"`
	Workflow    string             `json:"workflow"`
	Instance    string             `json:"instance"`
	Step        string             `json:"step"`
	Type        string             `json:"type"`
	Timeout     string             `json:"timeout,omitempty"`
	Facts       map[string]float64 `json:"facts,omitempty"`
	Status      string             `json:"status"`
	RequestedAt time.Time          `json:"requestedAt"`
	DecidedAt   *time.Time         `json:"decidedAt,omitempty"`
	Approver    *Approver          `json:"approver,omitempty"`
	Comment     string             `json:"comment,omitempty"`
}

func (a *Approval) key() string {
	return a.TenantID + "/" + a.Workload + "/" + a.Workflow + "/" + a.Instance + "/" + a.Step
}

// approvalsFormat is approvals.json's record version
// (ADR-041 §4): records without "v" are version 1, from
// before versioning; a newer version is not loaded.
const approvalsFormat = 1

// approvalRecord is one approvals.json record: the Approval plus "v".
type approvalRecord struct {
	V int `json:"v,omitempty"`
	*Approval
}

// approvalStore is the host's list, persisted in <data>/approvals.json so a
// wait survives a hostd restart (the instance itself is in DO storage).
type approvalStore struct {
	mu   sync.Mutex
	path string
	m    map[string]*Approval

	// deciding holds the keys of approvals whose decision is being
	// delivered (#561): one decision per approval.
	deciding map[string]bool
}

func (h *Host) approvals() *approvalStore {
	h.apprOnce.Do(func() {
		s := &approvalStore{path: filepath.Join(h.cfg.DataDir, "approvals.json"), m: map[string]*Approval{}}
		if b, err := os.ReadFile(s.path); err == nil {
			var list []approvalRecord
			if json.Unmarshal(b, &list) == nil {
				for _, r := range list {
					if r.V > approvalsFormat {
						h.log.Warn("approvals.json was written by a newer tailworks; upgrade: its waits are not loaded", "v", r.V)
						break
					}
					if r.Approval != nil {
						s.m[r.key()] = r.Approval
					}
				}
			}
		}
		h.appr = s
	})
	return h.appr
}

// saveLocked writes the list (callers hold s.mu), dropping the oldest
// decided records beyond maxDecidedApprovals.
func (s *approvalStore) saveLocked() {
	list := make([]*Approval, 0, len(s.m))
	var decided []*Approval
	for _, a := range s.m {
		list = append(list, a)
		if a.Status != ApprovalPending {
			decided = append(decided, a)
		}
	}
	if len(decided) > maxDecidedApprovals {
		sort.Slice(decided, func(i, j int) bool { return decided[i].RequestedAt.Before(decided[j].RequestedAt) })
		for _, a := range decided[:len(decided)-maxDecidedApprovals] {
			delete(s.m, a.key())
		}
		list = list[:0]
		for _, a := range s.m {
			list = append(list, a)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RequestedAt.Before(list[j].RequestedAt) })
	recs := make([]approvalRecord, len(list))
	for i, a := range list {
		recs[i] = approvalRecord{V: approvalsFormat, Approval: a}
	}
	b, _ := json.MarshalIndent(recs, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, s.path)
	}
}

// list returns copies of the approvals matching f, oldest first.
func (s *approvalStore) list(f func(*Approval) bool) []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Approval
	for _, a := range s.m {
		if f(a) {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out
}

// ListApprovals returns this host's approvals of tenantID ("" = all
// tenants); pendingOnly drops decided ones.
func (h *Host) ListApprovals(tenantID string, pendingOnly bool) []Approval {
	return h.approvals().list(func(a *Approval) bool {
		return (tenantID == "" || a.TenantID == tenantID) && (!pendingOnly || a.Status == ApprovalPending)
	})
}

// approvalReport records a wait the runtime reported (TailnetApi
// "approval", platform-checked: one of this app's Workflows, an existing
// instance). op "requested" is idempotent per wait; "timeout" settles it.
func (h *Host) approvalReport(inst *instance, raw json.RawMessage) (map[string]any, *apiError) {
	var r struct {
		Op       string             `json:"op"`
		Workflow string             `json:"workflow"`
		Instance string             `json:"instance"`
		Step     string             `json:"step"`
		Type     string             `json:"type"`
		Timeout  string             `json:"timeout"`
		Facts    map[string]float64 `json:"facts"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Instance == "" || r.Step == "" {
		return nil, &apiError{Code: "bad_request", Detail: "bad approval report"}
	}
	if inst.app.WorkflowByName(r.Workflow) == nil {
		return nil, &apiError{Code: "bad_request", Detail: "no Workflow " + r.Workflow + " in this deployment"}
	}
	wl := inst.w
	wl.mu.Lock()
	wlObj := wl.wl
	wl.mu.Unlock()
	objID := ""
	if wlObj != nil {
		objID = wlObj.ID
	}
	a := &Approval{TenantID: wl.tenantID, Workload: wl.name, Workflow: r.Workflow, Instance: r.Instance, Step: r.Step, Type: r.Type,
		Timeout: r.Timeout, Facts: r.Facts, Status: ApprovalPending, RequestedAt: time.Now().UTC()}
	s := h.approvals()
	s.mu.Lock()
	old := s.m[a.key()]
	var rec *mgmtapi.AuditRecord
	switch r.Op {
	case "requested":
		if old == nil {
			s.m[a.key()] = a
			rec = &mgmtapi.AuditRecord{Kind: kindApprovalRequested, Detail: approvalDetail(a, map[string]any{"reportedBy": "workflow runtime"})}
		} else {
			a = old
		}
	case "timeout":
		if old == nil {
			s.m[a.key()] = a
			old = a
		}
		a = old
		if a.Status == ApprovalPending {
			now := time.Now().UTC()
			a.Status, a.DecidedAt = ApprovalTimedOut, &now
			rec = &mgmtapi.AuditRecord{Kind: kindApprovalTimeout, Detail: approvalDetail(a, map[string]any{"decision": "reject", "reason": "timeout", "reportedBy": "workflow runtime"})}
		}
	default:
		s.mu.Unlock()
		return nil, &apiError{Code: "bad_request", Detail: "bad approval report op"}
	}
	if rec != nil {
		s.saveLocked()
	}
	view := *a
	s.mu.Unlock()
	if rec != nil {
		rec.ObjectID, rec.TraceID, rec.TenantID = objID, newTraceID(), wl.tenantID
		h.audit.add(*rec)
	}
	return map[string]any{"ok": true, "status": view.Status}, nil
}

func approvalDetail(a *Approval, extra map[string]any) json.RawMessage {
	d := map[string]any{"workload": a.Workload, "workflow": a.Workflow, "instance": a.Instance, "step": a.Step, "type": a.Type}
	if len(a.Facts) > 0 {
		d["facts"] = a.Facts
	}
	for k, v := range extra {
		d[k] = v
	}
	b, _ := json.Marshal(d)
	return b
}

func (a *Approval) target() approvecap.Target {
	return approvecap.Target{Workload: a.Workload, Workflow: a.Workflow, Step: a.Step, Facts: a.Facts}
}

// authorizeApproval checks grants (ApproveCap values) for deciding a with
// decision ("approve" | "reject"). It returns a cap_missing error naming
// what is missing, with the grant to add.
func authorizeApproval(grants []json.RawMessage, a *Approval, decision string, src string) *apiError {
	ok, why := approvecap.Check(grants, a.target(), decision)
	if ok {
		return nil
	}
	return &apiError{Code: "cap_missing", Detail: approvecap.Denied(src, decision, a.target(), len(grants), why), Extra: map[string]any{
		"missing": map[string]any{"cap": ApproveCap, "workload": a.Workload, "workflow": a.Workflow, "step": a.Step, "decision": decision},
		"snippet": approvecap.Snippet(src, a.target())}}
}

// approvalDecision is one decision, from either surface.
type approvalDecision struct {
	TenantID string            `json:"tenant"`
	Workload string            `json:"workload"`
	Workflow string            `json:"workflow"`
	Instance string            `json:"instance"`
	Step     string            `json:"step,omitempty"`
	Approve  bool              `json:"approve"`
	Comment  string            `json:"comment,omitempty"`
	Metadata map[string]any    `json:"metadata,omitempty"`
	Approver Approver          `json:"approver"`
	Grants   []json.RawMessage `json:"grants"`
}

const maxApprovalComment = 1024

// claim marks a as being decided, so a concurrent decision (another
// approver, or the same one retrying a request that is still being
// delivered) is refused instead of delivered too (#561). unclaim drops the
// mark: after a's status is set, or after a failed delivery so that a retry
// can decide it.
func (s *approvalStore) claim(a *Approval) *apiError {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.deciding[a.key()]:
		return &apiError{Code: "approval_not_pending", Detail: fmt.Sprintf("%s/%s step %q is being decided", a.Workflow, a.Instance, a.Step)}
	case a.Status != ApprovalPending:
		return &apiError{Code: "approval_not_pending", Detail: fmt.Sprintf("%s/%s step %q is %s", a.Workflow, a.Instance, a.Step, a.Status)}
	}
	if s.deciding == nil {
		s.deciding = map[string]bool{}
	}
	s.deciding[a.key()] = true
	return nil
}

func (s *approvalStore) unclaim(a *Approval) {
	s.mu.Lock()
	delete(s.deciding, a.key())
	s.mu.Unlock()
}

// decideApproval authorises, delivers and audits one decision.
func (h *Host) decideApproval(d approvalDecision) (*Approval, *apiError) {
	if d.Approver.NodeID == "" {
		return nil, &apiError{Code: "no_identity", Detail: "an approval needs a verified tailnet identity"}
	}
	if len(d.Comment) > maxApprovalComment {
		d.Comment = d.Comment[:maxApprovalComment]
	}
	h.mu.Lock()
	wl := h.workloads[d.TenantID+"/"+d.Workload]
	h.mu.Unlock()
	if wl == nil {
		return nil, &apiError{Code: "unknown_object", Detail: "workload " + d.Workload + " does not run here"}
	}
	s := h.approvals()
	s.mu.Lock()
	var cands []*Approval
	for _, a := range s.m {
		if a.TenantID == d.TenantID && a.Workload == d.Workload && a.Workflow == d.Workflow && a.Instance == d.Instance && (d.Step == "" || a.Step == d.Step) {
			cands = append(cands, a)
		}
	}
	var a *Approval
	var pending []*Approval
	for _, c := range cands {
		if c.Status == ApprovalPending {
			pending = append(pending, c)
		}
	}
	switch {
	case len(pending) == 1:
		a = pending[0]
	case len(pending) > 1:
		s.mu.Unlock()
		return nil, &apiError{Code: "bad_request", Detail: "the instance waits for several approvals; name the step"}
	case len(cands) > 0:
		c := *cands[0]
		s.mu.Unlock()
		return &c, &apiError{Code: "approval_not_pending", Detail: fmt.Sprintf("%s/%s step %q is %s", d.Workflow, d.Instance, c.Step, c.Status)}
	default:
		s.mu.Unlock()
		return nil, &apiError{Code: "approval_not_pending", Detail: fmt.Sprintf("no approval of %s/%s is waiting here", d.Workflow, d.Instance)}
	}
	view := *a
	s.mu.Unlock()

	decision := map[bool]string{true: "approve", false: "reject"}[d.Approve]
	wl.mu.Lock()
	objID := ""
	if wl.wl != nil {
		objID = wl.wl.ID
	}
	wl.mu.Unlock()
	audit := func(kind string, extra map[string]any) {
		ap := d.Approver
		extra["approver"] = ap
		h.audit.add(mgmtapi.AuditRecord{Kind: kind, ObjectID: objID, TenantID: d.TenantID, TraceID: newTraceID(), ActingFor: ap.principal(),
			Detail: approvalDetail(&view, extra)})
	}
	src := d.Approver.principal()
	if len(d.Approver.Tags) > 0 {
		src = d.Approver.Tags[0]
	}
	if e := authorizeApproval(d.Grants, &view, decision, src); e != nil {
		audit(kindApprovalDenied, map[string]any{"decision": decision, "code": e.Code})
		return nil, e
	}
	if e := s.claim(a); e != nil {
		return nil, e
	}
	defer s.unclaim(a) // after the status is set (success) or to let a retry decide (failure)

	// The payload the Workflow sees. "approver" and metadata.approver are
	// tailworks' (the identity above), whatever the request carried.
	meta := map[string]any{}
	for k, v := range d.Metadata {
		if k != "approver" {
			meta[k] = v
		}
	}
	meta["approver"] = d.Approver
	if d.Comment != "" {
		meta["comment"] = d.Comment
	}
	payload := map[string]any{"approved": d.Approve, "approver": d.Approver, "metadata": meta, "decidedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	if d.Comment != "" {
		payload["comment"] = d.Comment
		if !d.Approve {
			payload["reason"] = d.Comment
		}
	} else if !d.Approve {
		payload["reason"] = "rejected by " + d.Approver.principal()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	inst, release := wl.acquire(ctx)
	cancel()
	if inst == nil {
		return nil, &apiError{Code: "upstream_unavailable", Detail: "workload " + d.Workload + " has no running instance"}
	}
	code, body := inst.admin(map[string]any{"kind": "approval", "workflow": view.Workflow, "instance": view.Instance, "type": view.Type, "payload": payload})
	release()
	if code != 200 {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		json.Unmarshal(body, &e)
		if e.Code == "" {
			e.Code = "upstream_unavailable"
		}
		return nil, &apiError{Code: e.Code, Detail: "approval delivery: " + e.Message}
	}

	s.mu.Lock()
	now := time.Now().UTC()
	a.Status = map[bool]string{true: ApprovalApproved, false: ApprovalRejected}[d.Approve]
	a.DecidedAt, a.Comment = &now, d.Comment
	ap := d.Approver
	a.Approver = &ap
	s.saveLocked()
	view = *a
	s.mu.Unlock()
	extra := map[string]any{"decision": decision}
	if d.Comment != "" {
		extra["comment"] = d.Comment
	}
	audit(kindApprovalDecision, extra)
	return &view, nil
}

// ---- the approval endpoint on a workload's / object's node ----

// approvalsPath is the prefix the platform never routes to tenant code.
const approvalsPath = "/_tw/approvals"

// serveApprovals handles /_tw/approvals[/<workflow>/<instance>] for a
// request netd delivered (blob: its cf, with the verified caller). It
// reports whether it handled the request.
//
//	GET  /_tw/approvals                         the pending approvals of this workload the caller may decide
//	GET  /_tw/approvals/<workflow>/<instance>   one instance's approvals (any status)
//	POST /_tw/approvals/<workflow>/<instance>   {"decision": "approve"|"reject", "comment", "metadata", "step"}
//
// A POST must be Content-Type application/json: a cross-site page cannot
// send one without a CORS preflight, which this endpoint never grants, so
// a browser on the tailnet cannot be made to approve from another site.
func (h *Host) serveApprovals(w http.ResponseWriter, r *http.Request, wl *workload, blob map[string]any) bool {
	p := r.URL.Path
	if p != approvalsPath && !strings.HasPrefix(p, approvalsPath+"/") {
		return false
	}
	if publicBlob(blob) {
		writeAPIError(w, &apiError{Code: "public_request", Detail: "approvals are for tailnet identities, never public requests"})
		return true
	}
	ap, grants, err := callerApprover(blob)
	if err != nil {
		writeAPIError(w, err)
		return true
	}
	rest := strings.Trim(strings.TrimPrefix(p, approvalsPath), "/")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 0:
		mine := []Approval{}
		for _, a := range h.ListApprovals(wl.tenantID, true) {
			if a.Workload == wl.name && mayDecide(grants, &a) {
				mine = append(mine, a)
			}
		}
		if len(mine) == 0 && len(grants) == 0 {
			writeAPIError(w, &apiError{Code: "cap_missing", Detail: "no " + ApproveCap + " grant toward this node", Extra: map[string]any{"missing": map[string]any{"cap": ApproveCap}}})
			return true
		}
		writeJSON(w, 200, map[string]any{"approvals": mine, "approver": ap})
	case r.Method == http.MethodGet && len(parts) == 2:
		out := []Approval{}
		for _, a := range h.ListApprovals(wl.tenantID, false) {
			if a.Workload == wl.name && a.Workflow == parts[0] && a.Instance == parts[1] {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			writeAPIError(w, &apiError{Code: "approval_not_pending", Detail: "no approval of " + parts[0] + "/" + parts[1] + " here"})
			return true
		}
		if !mayDecide(grants, &out[0]) {
			writeAPIError(w, authorizeApproval(grants, &out[0], "approve", ap.principal()))
			return true
		}
		writeJSON(w, 200, map[string]any{"approvals": out})
	case r.Method == http.MethodPost && len(parts) == 2:
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: "an approval is a JSON POST (Content-Type: application/json)"})
			return true
		}
		var body struct {
			Decision string         `json:"decision"`
			Comment  string         `json:"comment"`
			Metadata map[string]any `json:"metadata"`
			Step     string         `json:"step"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: "bad JSON: " + err.Error()})
			return true
		}
		if body.Decision != "approve" && body.Decision != "reject" {
			writeAPIError(w, &apiError{Code: "bad_request", Detail: `decision must be "approve" or "reject"`})
			return true
		}
		a, e := h.decideApproval(approvalDecision{TenantID: wl.tenantID, Workload: wl.name, Workflow: parts[0], Instance: parts[1], Step: body.Step,
			Approve: body.Decision == "approve", Comment: body.Comment, Metadata: body.Metadata, Approver: ap, Grants: grants})
		if e != nil {
			writeAPIError(w, e)
			return true
		}
		writeJSON(w, 200, map[string]any{"ok": true, "approval": a})
	case r.Method == http.MethodOptions:
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "no cross-origin access to approvals"})
	default:
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "GET " + approvalsPath + "[/<workflow>/<instance>] or POST " + approvalsPath + "/<workflow>/<instance>"})
	}
	return true
}

// mayDecide: some decision on a is open to these grants (who may see it).
func mayDecide(grants []json.RawMessage, a *Approval) bool {
	return approvecap.MayDecide(grants, a.target())
}

// callerApprover reads the verified caller (netd's cf.tailscale.caller)
// and its ApproveCap grants toward this node.
func callerApprover(blob map[string]any) (Approver, []json.RawMessage, *apiError) {
	ts, _ := blob["tailscale"].(map[string]any)
	raw, _ := json.Marshal(ts["caller"])
	var c netd.Caller
	if err := json.Unmarshal(raw, &c); err != nil || c.NodeID == "" {
		return Approver{}, nil, &apiError{Code: "no_identity", Detail: "request has no verified tailnet caller"}
	}
	if c.CapsWithheld {
		return Approver{}, nil, &apiError{Code: "cap_missing", Detail: "the caller's capabilities were withheld (certificate lookup failed); try again"}
	}
	ap := Approver{User: c.User, Node: c.Node, NodeID: c.NodeID, Tags: c.Tags, Via: "tailnet"}
	if ap.Tags == nil {
		ap.Tags = []string{}
	}
	return ap, c.CapMap[ApproveCap], nil
}

// ---- push API (mgmt → hostd): tw approvals / tw approve / admin UI ----

// approvalsListHandler is GET /v1/approvals?tenant=&all=1.
func (h *Host) approvalsListHandler(w http.ResponseWriter, r *http.Request) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "tenant required"})
		return
	}
	list := h.ListApprovals(tenant, r.URL.Query().Get("all") == "")
	if list == nil {
		list = []Approval{}
	}
	writeJSON(w, 200, map[string]any{"approvals": list})
}

// approvalsDecideHandler is POST /v1/approvals/decide (approvalDecision,
// Approver.Via "mgmt"): mgmt has authenticated the approver by WhoIs and
// passes their ApproveCap grants toward mgmt.
func (h *Host) approvalsDecideHandler(w http.ResponseWriter, r *http.Request) {
	var d approvalDecision
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&d); err != nil {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "bad JSON: " + err.Error()})
		return
	}
	d.Approver.Via = "mgmt"
	if d.Approver.Tags == nil {
		d.Approver.Tags = []string{}
	}
	a, e := h.decideApproval(d)
	if e != nil {
		writeAPIError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "approval": a})
}
