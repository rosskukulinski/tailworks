package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/rosskukulinski/twd/internal/auditfmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Approvals (ADR-040): /ui/approvals lists the Workflow
// approvals waiting in every tenant the caller can list (or ?tenant=, for
// an approver who is not a twd admin) and offers Approve / Reject. The
// decision is POST /v1/tenants/{id}/workloads/{w}/approvals/{wf}/{instance}
// made with the browser's own tailnet identity (mgmt's WhoIs of this
// connection); the host running the Workflow checks the caller's
// twd.dev/cap/approve grants, so an admin without a grant is refused
// (cap_missing) like anyone else.

type approver struct {
	User   string   `json:"user"`
	Node   string   `json:"node"`
	NodeID string   `json:"nodeId"`
	Tags   []string `json:"tags"`
	Via    string   `json:"via"`
}

type approval struct {
	TenantID    string             `json:"tenantID"`
	Workload    string             `json:"workload"`
	Workflow    string             `json:"workflow"`
	Instance    string             `json:"instance"`
	Step        string             `json:"step"`
	Type        string             `json:"type"`
	Timeout     string             `json:"timeout"`
	Facts       map[string]float64 `json:"facts"`
	Status      string             `json:"status"`
	RequestedAt time.Time          `json:"requestedAt"`
	DecidedAt   *time.Time         `json:"decidedAt"`
	Approver    *approver          `json:"approver"`
	Comment     string             `json:"comment"`
	Host        string             `json:"host"`
}

// Age is how long the approval has waited (or waited until decided).
func (a approval) Age() string {
	end := time.Now()
	if a.DecidedAt != nil {
		end = *a.DecidedAt
	}
	return ago(a.RequestedAt.UnixMilli(), end)
}

// fact is one of the instance's numeric params.
type fact struct {
	Name  string
	Value string
}

// FactList is the instance's numeric params, sorted by name (rendered as
// a definition list).
func (a approval) FactList() []fact {
	keys := make([]string, 0, len(a.Facts))
	for k := range a.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]fact, 0, len(keys))
	for _, k := range keys {
		out = append(out, fact{k, fmt.Sprint(a.Facts[k])})
	}
	return out
}

// FactsText renders the instance's numeric params on one line.
func (a approval) FactsText() string {
	var parts []string
	for _, f := range a.FactList() {
		parts = append(parts, f.Name+"="+f.Value)
	}
	return strings.Join(parts, " ")
}

// Anchor is the row's id: the deep link of an approval_requested
// notification is /ui/approvals?tenant=…#<instance>.
func (a approval) Anchor() string { return a.Instance }

// whoTenant is a tenant GET /v1/whoami says the caller can act in.
type whoTenant struct {
	TenantID string `json:"tenantID"`
	Tailnet  string `json:"tailnet"`
}

// approveGrant is one of the caller's twd.dev/cap/approve grant values.
type approveGrant struct {
	Workloads []string           `json:"workloads"`
	Workflows []string           `json:"workflows"`
	Steps     []string           `json:"steps"`
	Decisions []string           `json:"decisions"`
	Limits    map[string]float64 `json:"limits"`
}

// Text describes the grant in words ("any approval" when it does not
// narrow).
func (g approveGrant) Text() string {
	var parts []string
	list := func(what string, l []string) {
		if len(l) > 0 && !(len(l) == 1 && l[0] == "*") {
			parts = append(parts, what+" "+strings.Join(l, ", "))
		}
	}
	list("apps", g.Workloads)
	list("Workflows", g.Workflows)
	list("steps", g.Steps)
	list("decisions", g.Decisions)
	keys := make([]string, 0, len(g.Limits))
	for k := range g.Limits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ≤ %v", k, g.Limits[k]))
	}
	if len(parts) == 0 {
		return "any approval"
	}
	return strings.Join(parts, "; ")
}

// DecidePath is the /v1 route that decides this approval.
func (a approval) DecidePath() string {
	return "/v1/tenants/" + url.PathEscape(a.TenantID) + "/workloads/" + url.PathEscape(a.Workload) + "/approvals/" + url.PathEscape(a.Workflow) + "/" + url.PathEscape(a.Instance)
}

type approvalsData struct {
	base
	Tenant    string      // ?tenant=
	All       bool        // ?all=1
	Tenants   []whoTenant // the tenants the page defaults to
	Names     map[string]string
	TenantBox bool // more (or fewer) than one tenant to choose from
	Approvals []approval
	Errors    []string
}

// TenantName is a tenant's tailnet when known, else its ID.
func (d approvalsData) TenantName(id string) string {
	if n := d.Names[id]; n != "" {
		return n
	}
	return id
}

// approvals lists the approvals of ?tenant=, or by default of the tenants
// the caller can act in: every tenant for an admin, else the tenants on
// the caller's own tailnet (GET /v1/whoami), so an approver never needs
// to know a tenant ID. The tenant box is hidden when there is exactly one.
func (u *ui) approvals(w http.ResponseWriter, r *http.Request) {
	d := approvalsData{base: u.base(r, "approvals"), Tenant: r.URL.Query().Get("tenant"), All: r.URL.Query().Get("all") != "", Names: map[string]string{}}
	if d.Who.Admin {
		ts, err := listAll[tenant](u, r, "/v1/tenants?limit=500", 2000)
		if err != nil {
			d.Errors = append(d.Errors, "tenants: "+err.Error())
		}
		for _, t := range ts {
			d.Tenants = append(d.Tenants, whoTenant{TenantID: t.TenantID, Tailnet: t.Tailnet})
		}
	}
	if len(d.Tenants) == 0 {
		d.Tenants = d.Who.Tenants
	}
	for _, t := range d.Tenants {
		d.Names[t.TenantID] = t.Tailnet
	}
	d.TenantBox = len(d.Tenants) != 1
	var ids []string
	switch {
	case d.Tenant != "":
		ids = []string{d.Tenant}
	case len(d.Tenants) == 0:
		tn := d.Who.Tailnet
		if tn == "" {
			tn = "your tailnet"
		}
		d.Errors = append(d.Errors, "No tenant uses "+tn+", so there is nothing to show by default. Enter the tenant ID from your approval link or ask your twd admin for it.")
	default:
		for _, t := range d.Tenants {
			ids = append(ids, t.TenantID)
		}
	}
	for _, id := range ids {
		var out struct {
			Approvals   []approval `json:"approvals"`
			Unreachable []string   `json:"unreachable"`
		}
		q := ""
		if d.All {
			q = "?all=true"
		}
		if err := u.call(r, "GET", "/v1/tenants/"+url.PathEscape(id)+"/approvals"+q, &out); err != nil {
			d.Errors = append(d.Errors, d.TenantName(id)+": "+err.Error())
			continue
		}
		d.Approvals = append(d.Approvals, out.Approvals...)
		for _, h := range out.Unreachable {
			d.Errors = append(d.Errors, d.TenantName(id)+": host "+h+" did not answer; its approvals are missing")
		}
	}
	sort.SliceStable(d.Approvals, func(i, j int) bool { return d.Approvals[i].RequestedAt.Before(d.Approvals[j].RequestedAt) })
	u.render(w, 200, "approvals", d)
}

// decideForm is the no-JavaScript fallback of the Approve / Reject forms.
func (u *ui) decideForm(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f := r.PostForm
	a := approval{TenantID: f.Get("tenant"), Workload: f.Get("workload"), Workflow: f.Get("workflow"), Instance: f.Get("instance")}
	body := map[string]any{"decision": f.Get("decision")}
	if c := strings.TrimSpace(f.Get("comment")); c != "" {
		body["comment"] = c
	}
	back := "/ui/approvals"
	if t := f.Get("back_tenant"); t != "" {
		back += "?tenant=" + url.QueryEscape(t)
	}
	b, _ := json.Marshal(body)
	err := u.callBody(r, "POST", a.DecidePath(), b, nil)
	to, _ := url.Parse(back)
	q := to.Query()
	if err != nil {
		q.Set("err", err.Error())
	} else {
		q.Set("ok", fmt.Sprintf("%s/%s/%s: %s", a.Workload, a.Workflow, a.Instance, auditfmt.Decided(f.Get("decision"))))
	}
	to.RawQuery = q.Encode()
	to.Fragment = a.Anchor()
	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

// callBody is call with a JSON request body.
func (u *ui) callBody(r *http.Request, method, p string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(r.Context(), method, u.apiURL+p, bytes.NewReader(body))
	if err != nil {
		return &apiError{Status: 500, Code: "internal", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	res, err := u.api.Do(req)
	if err != nil {
		return &apiError{Status: 502, Code: "internal", Message: fmt.Sprintf("%s %s: %v", method, p, err)}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if res.StatusCode/100 != 2 {
		e := &apiError{Status: res.StatusCode, Raw: b}
		if json.Unmarshal(b, e) != nil || e.Code == "" {
			e.Code, e.Message = "internal", fmt.Sprintf("%s %s answered %d", method, p, res.StatusCode)
		}
		return e
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// approvalsPage: the approvals pages are open to approvers who are not
// admins (mgmt's own gate lets them reach these paths only; the /v1 role
// gate decides what each call may do).
func approvalsPage(path string) bool {
	return path == "/ui/approvals" || path == "/ui/approvals/decide"
}
