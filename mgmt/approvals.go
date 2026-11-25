package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Workflow approvals through mgmt (ADR-040): `tw
// approvals`, `tw approve` and the admin UI. The approvals live on the
// hosts that run the Workflows; mgmt reads and forwards them over the same
// mgmt → hostd channel as kills and logs (GET /v1/approvals, POST
// /v1/approvals/decide on the push API). The host authorises the decision
// (the caller's github.com/rosskukulinski/tailworks/cap/approve grants toward mgmt, which mgmt sends
// with the caller's WhoIs identity), delivers it and audits it.

// isApprover reports whether a tailnet caller holds github.com/rosskukulinski/tailworks/cap/approve
// toward mgmt (admitted to the approvals routes only, unless also admin).
func isApprover(p Principal) bool {
	return slices.Contains(p.Caps, approvecap.Cap) || tenantApprovers(p) // tenantscope_approve.go
}

// approvalTimeout bounds one mgmt → hostd approvals call.
const approvalTimeout = 15 * time.Second

// hostDo sends one request to hostID's push API (unix socket, the host's
// tailnet node, or the host agent in this process).
func (m *Mgmt) hostDo(ctx context.Context, hostID, method, path string, body []byte) (*http.Response, error) {
	open := func(target string) (*http.Response, error) {
		base := "http://hostd"
		var dial func(ctx context.Context, network, addr string) (net.Conn, error)
		if sock, ok := strings.CutPrefix(target, "unix://"); ok {
			dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}
		} else {
			base, dial = target, m.cfg.TailnetDial
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
		}
		var tr http.RoundTripper = &http.Transport{DialContext: dial, DisableKeepAlives: true}
		if target == EmbeddedPushURL {
			m.mu.Lock()
			tr, base = handlerTransport{m.inprocPush[hostID]}, "http://hostd"
			m.mu.Unlock()
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return (&http.Client{Transport: tr, Timeout: approvalTimeout}).Do(req)
	}
	u := m.pushTarget(hostID)
	if u == "" {
		return nil, errors.New("host has no push URL")
	}
	resp, err := open(u)
	if err != nil && ctx.Err() == nil {
		m.mu.Lock()
		delete(m.pushURLs, hostID)
		m.mu.Unlock()
		if u2 := m.pushTarget(hostID); u2 != "" && u2 != u {
			resp, err = open(u2)
		}
	}
	return resp, err
}

// hostError turns a hostd error body into an API error.
func hostError(resp *http.Response) *Error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Missing map[string]any `json:"missing"`
		Snippet string         `json:"snippet"`
	}
	json.Unmarshal(b, &e)
	status := resp.StatusCode
	switch e.Code {
	case "cap_missing", "no_identity":
		status = 403
	case "approval_not_pending":
		status = 409
	case "unknown_object":
		e.Code, status = "not_found", 404
	case "":
		e.Code, e.Message, status = "host_unavailable", "host answered "+resp.Status, 502
	default:
		if status < 400 {
			status = 502
		}
	}
	out := errf(status, e.Code, "%s", e.Message)
	out.Missing, out.Snippet = e.Missing, e.Snippet
	return out
}

// ListApprovals merges the tenant's approvals from every live host.
func (m *Mgmt) ListApprovals(ctx context.Context, tenantID, workload string, all bool) (out v1.ApprovalList, err error) {
	out = v1.ApprovalList{Approvals: []v1.Approval{}}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return out, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	hosts, err := m.store.ListHosts(ctx, Page{})
	if err != nil {
		return out, err
	}
	var unreachable []string
	defer func() {
		if len(unreachable) > 0 {
			out.Unreachable = &unreachable
		}
	}()
	q := url.Values{"tenant": {tenantID}}
	if all {
		q.Set("all", "1")
	}
	for _, h := range hosts {
		if m.pushTarget(h.ID) == "" {
			continue
		}
		resp, err := m.hostDo(ctx, h.ID, "GET", "/v1/approvals?"+q.Encode(), nil)
		if err != nil {
			unreachable = append(unreachable, h.ID)
			continue
		}
		var page struct {
			Approvals []v1.Approval `json:"approvals"`
		}
		ok := resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page) == nil
		resp.Body.Close()
		if !ok {
			unreachable = append(unreachable, h.ID)
			continue
		}
		for _, a := range page.Approvals {
			if workload != "" && a.Workload != workload {
				continue
			}
			hid := h.ID
			a.Host = &hid
			out.Approvals = append(out.Approvals, a)
		}
	}
	sort.SliceStable(out.Approvals, func(i, j int) bool { return out.Approvals[i].RequestedAt.Before(out.Approvals[j].RequestedAt) })
	return out, nil
}

// approverOf is the identity mgmt vouches for: its WhoIs of the caller.
func approverOf(p Principal) map[string]any {
	user := ""
	if len(p.Tags) == 0 {
		user = p.Login
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{"user": user, "node": p.Name, "nodeId": p.NodeID, "tags": tags, "via": "mgmt"}
}

// DecideApproval forwards one decision, made by p, to the host running the
// instance (the workload's, or one of its replicas', lease holder).
func (m *Mgmt) DecideApproval(ctx context.Context, p Principal, tenantID, workload, workflow, instance string, req v1.ApprovalDecisionRequest) (*v1.Approval, error) {
	if p.Kind != "user" || p.NodeID == "" {
		return nil, errf(403, "no_identity", "an approval is a decision by your own tailnet identity; this caller (%s) has none: run `tw link` on your machine, or approve on the workload's node", p.Kind)
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return nil, errf(400, "bad_request", `decision must be "approve" or "reject"`)
	}
	srcs, err := m.logSources(ctx, tenantID, workload, "")
	if err != nil {
		return nil, err
	}
	grants := approveGrantsFor(p, tenantID) // tenantscope_approve.go
	var last error = errf(409, "approval_not_pending", "no approval of %s/%s is waiting", workflow, instance)
	for _, src := range srcs {
		body, _ := json.Marshal(map[string]any{
			"tenant": tenantID, "workload": src.workload, "workflow": workflow, "instance": instance, "step": str(req.Step),
			"approve": req.Decision == "approve", "comment": str(req.Comment), "metadata": req.Metadata,
			"approver": approverOf(p), "grants": grants,
		})
		resp, err := m.hostDo(ctx, src.host, "POST", "/v1/approvals/decide", body)
		if err != nil {
			last = errf(502, "host_unavailable", "host %s: %v", src.host, err)
			continue
		}
		if resp.StatusCode != 200 {
			e := hostError(resp)
			resp.Body.Close()
			last = e
			if e.Code == "approval_not_pending" || e.Code == "not_found" {
				continue // another replica may run it
			}
			return nil, e
		}
		var r struct {
			Approval v1.Approval `json:"approval"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return nil, errf(502, "host_unavailable", "host %s: %v", src.host, err)
		}
		h := src.host
		r.Approval.Host = &h
		m.log.Info("approval decided", "tenant", tenantID, "workload", src.workload, "workflow", workflow, "instance", instance, "decision", req.Decision, "by", p.Login, "node", p.NodeID)
		return &r.Approval, nil
	}
	return nil, last
}

// ---- /v1 handlers ----

func (a *v1api) ListApprovals(w http.ResponseWriter, r *http.Request, id v1.TenantID, params v1.ListApprovalsParams) {
	p := principalOf(r.Context())
	if err := a.s.tenantApproveOK(p, id); err != nil {
		writeErr(w, r, err)
		return
	}
	out, err := a.s.M.ListApprovals(r.Context(), id, str(params.Workload), params.All != nil && *params.All)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if p.Kind == "user" && !a.s.allowed(p, roleAdmin) {
		// An approver who is not an admin sees what their grants let them decide.
		mine := out.Approvals[:0]
		for _, ap := range out.Approvals {
			facts := map[string]float64{}
			if ap.Facts != nil {
				facts = *ap.Facts
			}
			if approvecap.MayDecide(approveGrantsFor(p, id), approvecap.Target{Workload: ap.Workload, Workflow: ap.Workflow, Step: ap.Step, Facts: facts}) {
				mine = append(mine, ap)
			}
		}
		out.Approvals = mine
	}
	writeJSON(w, 200, out)
}

func (a *v1api) DecideApproval(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, workflow string, instance string) {
	var req v1.ApprovalDecisionRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.s.tenantApproveOK(principalOf(r.Context()), id); err != nil {
		writeErr(w, r, err)
		return
	}
	ap, err := a.s.M.DecideApproval(r.Context(), principalOf(r.Context()), id, workload, workflow, instance, req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v1.ApprovalDecisionResponse{Approval: *ap})
}
