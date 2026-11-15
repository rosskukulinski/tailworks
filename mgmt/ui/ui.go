// Package ui is twd-mgmt's embedded admin web UI: Go html/template pages
// plus one CSS file and one vanilla-JS file, served under /ui/ by mgmt
// itself (no build step, no external assets, nothing stored client-side).
//
// The UI is an ordinary client of the public /v1 API. It uses only what
// api/openapi.yaml documents: its operations, query parameters, headers
// and JSON (TestUsesOnlyPublicAPI enforces this). It never imports mgmt,
// and it has no back door: it decides who may use it from GET /v1/whoami,
// like the CLI. That keeps it extractable into its own process. Whose
// identity a call carries is the transport's business: mgmt's embedded
// mount serves the calls in-process as the browser's principal
// (mgmt/uiclient.go), so the role gate applies exactly as to any caller.
//
// The browser only ever talks to the UI. app.js re-fetches the page every
// 5 s to refresh the live regions and asks for confirmation before a
// mutation; the mutation itself is a plain form POST to /ui/..., which
// makes the /v1 call server-side and redirects back with the outcome.
package ui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Handler serves the admin UI under /ui/. It sends its /v1 requests with
// api to baseURL (for example "https://twd-mgmt.example.ts.net"), each with
// the browser request's context. Only admins that are not hosts may use
// it, as GET /v1/whoami reports them.
func Handler(api *http.Client, baseURL string) http.Handler {
	u := &ui{api: api, apiURL: strings.TrimSuffix(baseURL, "/"), pages: parsePages(), now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/{$}", u.overview)
	mux.HandleFunc("GET /ui/objects/{id}", u.object)
	mux.HandleFunc("POST /ui/objects/{id}/kill", u.killForm)
	mux.HandleFunc("GET /ui/deployments/{id}", u.deployment)
	mux.HandleFunc("POST /ui/deployments/{id}/undeploy", u.undeployForm)
	mux.HandleFunc("GET /ui/hosts/{id}", u.host)
	mux.HandleFunc("POST /ui/hosts/{id}/remove", u.removeHostForm)
	mux.HandleFunc("GET /ui/tenants/{id}", u.tenant)
	mux.HandleFunc("POST /ui/tenants/{id}/delete", u.deleteTenantForm)
	mux.HandleFunc("GET /ui/audit", u.audit)
	// approvals.go
	mux.HandleFunc("GET /ui/approvals", u.approvals)
	mux.HandleFunc("POST /ui/approvals/decide", u.decideForm)
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServerFS(static)))
	mux.HandleFunc("/ui/", u.notFound)
	return secureHeaders(u.admit(mux))
}

// secureHeaders sets the response headers every UI response carries.
func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cache-Control", "no-store")
		if r.URL.Path == "/ui" {
			http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			return
		}
		h.ServeHTTP(w, r)
	})
}

type ui struct {
	api    *http.Client
	apiURL string // the API base URL, no trailing slash
	pages  map[string]*template.Template
	now    func() time.Time
}

type whoKey struct{}

// admit lets a request through only when GET /v1/whoami says the caller is
// an admin and not a host. The answer rides on the request context so the
// page header doesn't ask again. Static assets hold no data and skip the
// check.
func (u *ui) admit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ui/static/") {
			next.ServeHTTP(w, r)
			return
		}
		var who whoAmI
		if err := u.call(r, "GET", "/v1/whoami", &who); err != nil {
			u.fail(w, r, err)
			return
		}
		if who.Kind == "host" || !who.Admin && !approvalsPage(r.URL.Path) { // approvals.go
			u.fail(w, r, &apiError{Status: 403, Code: "forbidden", Message: fmt.Sprintf("caller (%s) may not use the admin UI", who.Kind)})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), whoKey{}, who)))
	})
}

// ---- /v1 client ----

// maxBody bounds an API answer the UI will read (a 500-item page of
// objects is well under it).
const maxBody = 32 << 20

// apiError is the /v1 typed error (docs/errors.md) as the UI sees it.
type apiError struct {
	Status    int             `json:"-"`
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	DocURL    string          `json:"doc_url"`
	TraceID   string          `json:"trace_id"`
	Snippet   string          `json:"snippet"`
	Tombstone *tombstone      `json:"tombstone"` // 410 killed
	Raw       json.RawMessage `json:"-"`
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// call sends method path to the API with the browser request's context and
// decodes a 2xx body into out. Non-2xx answers are *apiError.
func (u *ui) call(r *http.Request, method, p string, out any) error {
	req, err := http.NewRequestWithContext(r.Context(), method, u.apiURL+p, nil)
	if err != nil {
		return &apiError{Status: 500, Code: "internal", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	// Twd-Actor names the actor on the local-dev socket, as the CLI does;
	// over the tailnet the caller's WhoIs login wins.
	req.Header.Set("Twd-Actor", "operator (admin ui)")
	res, err := u.api.Do(req)
	if err != nil {
		return &apiError{Status: 502, Code: "internal", Message: fmt.Sprintf("%s %s: %v", method, p, err)}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return &apiError{Status: 502, Code: "internal", Message: fmt.Sprintf("%s %s: reading the answer: %v", method, p, err)}
	}
	if res.StatusCode/100 != 2 {
		e := &apiError{Status: res.StatusCode, Raw: body}
		if json.Unmarshal(body, e) != nil || e.Code == "" {
			e.Code = "internal"
			e.Message = fmt.Sprintf("%s %s answered %d", method, p, res.StatusCode)
		}
		return e
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return &apiError{Status: 500, Code: "internal", Message: fmt.Sprintf("%s %s: %v", method, p, err)}
		}
	}
	return nil
}

// listAll follows nextCursor up to maxItems (0 = one page).
func listAll[T any](u *ui, r *http.Request, p string, maxItems int) ([]T, error) {
	var all []T
	next := ""
	for {
		q := p
		if next != "" {
			sep := "?"
			if strings.Contains(q, "?") {
				sep = "&"
			}
			q += sep + "cursor=" + url.QueryEscape(next)
		}
		var pg page[T]
		if err := u.call(r, "GET", q, &pg); err != nil {
			return all, err
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" || maxItems == 0 || len(all) >= maxItems {
			return all, nil
		}
		next = *pg.NextCursor
	}
}

// ---- templates ----

func parsePages() map[string]*template.Template {
	funcs := template.FuncMap{
		"ago":      func(ms int64) string { return ago(ms, time.Now()) },
		"since":    func(ms int64) template.HTML { return since(ms, time.Now()) },
		"stamp":    stamp,
		"stampStr": stampStr,
		"short":    short,
		"ttl":      ttl,
		"badge":    badge,
		"join":     func(s []string) string { return strings.Join(s, ", ") },
		"pretty":   prettyJSON,
		"describe": describe,
		// errtext.go: a deployment error folded for display.
		"errLines":   errLines,
		"errSummary": errSummary,
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"anyJSON": func(v any) string {
			b, _ := json.MarshalIndent(v, "", "  ")
			return string(b)
		},
		"seqDecisions": func() []string { return []string{"approve", "reject"} },
		"auditSet": func(recs []auditRecord, names map[string]string) any {
			return struct {
				Records []auditRecord
				Names   map[string]string
			}{recs, names}
		},
	}
	layout := template.Must(template.New("layout").Funcs(funcs).ParseFS(templateFS, "templates/layout.html"))
	pages := map[string]*template.Template{}
	for _, name := range []string{"overview", "object", "deployment", "host", "tenant", "audit", "approvals", "error"} {
		t := template.Must(layout.Clone())
		pages[name] = template.Must(t.ParseFS(templateFS, "templates/"+name+".html"))
	}
	return pages
}

// base is what every page gets (the layout's header and flash).
type base struct {
	Title    string
	Path     string
	Who      whoAmI
	Status   status
	Flash    string // ?ok=
	FlashErr string // ?err=
	Now      string
}

func (u *ui) base(r *http.Request, title string) base {
	b := base{Title: title, Path: r.URL.Path, Now: stamp(u.now().UnixMilli())}
	if who, ok := r.Context().Value(whoKey{}).(whoAmI); ok {
		b.Who = who
	} else {
		u.call(r, "GET", "/v1/whoami", &b.Who)
	}
	u.call(r, "GET", "/v1/status", &b.Status)
	b.Flash = r.URL.Query().Get("ok")
	b.FlashErr = r.URL.Query().Get("err")
	return b
}

// render executes page with data; a template error is a 500 with the
// message (never a half-written page).
func (u *ui) render(w http.ResponseWriter, status int, page string, data any) {
	var buf bytes.Buffer
	if err := u.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "template "+page+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// fail renders the error page for an API error (its status), or 500.
func (u *ui) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = &apiError{Status: 500, Code: "internal", Message: err.Error()}
	}
	if ae.Status == 0 {
		ae.Status = 500
	}
	w.Header().Set("Twd-Error", ae.Code)
	u.render(w, ae.Status, "error", struct {
		base
		Err *apiError
	}{u.base(r, ae.Code), ae})
}

func (u *ui) notFound(w http.ResponseWriter, r *http.Request) {
	u.fail(w, r, &apiError{Status: 404, Code: "not_found", Message: r.Method + " " + r.URL.Path + " is not a UI page"})
}

// ---- pages ----

type overviewData struct {
	base
	Hosts        []host
	Tenants      []tenant
	Deployments  []deployment
	Objects      []object
	Names        map[string]string
	Audit        []auditRecord
	Verification verification
	MintModes    []string
	Errors       []string
}

func (u *ui) overview(w http.ResponseWriter, r *http.Request) {
	d := overviewData{base: u.base(r, "overview"), Names: map[string]string{}}
	note := func(what string, err error) {
		if err != nil {
			d.Errors = append(d.Errors, what+": "+err.Error())
		}
	}
	var err error
	d.Hosts, err = listAll[host](u, r, "/v1/hosts?limit=500", 2000)
	note("hosts", err)
	d.Tenants, err = listAll[tenant](u, r, "/v1/tenants?limit=500", 2000)
	note("tenants", err)
	seenMode := map[string]bool{}
	for _, t := range d.Tenants {
		if !seenMode[t.MintMode] {
			seenMode[t.MintMode] = true
			d.MintModes = append(d.MintModes, t.MintMode)
		}
	}
	sort.Strings(d.MintModes)
	d.Deployments, err = listAll[deployment](u, r, "/v1/deployments?latest=true&limit=500", 2000)
	note("deployments", err)
	objs, err := listAll[object](u, r, "/v1/objects?limit=500", 4000)
	note("objects", err)
	for _, o := range objs {
		d.Names[o.ObjectID] = o.NodeName()
	}
	d.Objects = buildTree(objs)
	d.Audit, err = u.recentAudit(r, 25)
	note("audit", err)
	note("audit verification", u.call(r, "GET", "/v1/audit/verification", &d.Verification))
	u.render(w, 200, "overview", d)
}

// recentAudit returns the newest n records. The audit list is oldest
// first with cursor paging, so this asks for the last 24 hours (one page
// of 500) and keeps the tail, falling back to the unbounded first page.
func (u *ui) recentAudit(r *http.Request, n int) ([]auditRecord, error) {
	since := u.now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	var pg auditPage
	if err := u.call(r, "GET", "/v1/audit?limit=500&since="+url.QueryEscape(since), &pg); err != nil {
		return nil, err
	}
	recs := pg.Items
	if len(recs) == 0 {
		if err := u.call(r, "GET", "/v1/audit?limit=500", &pg); err != nil {
			return nil, err
		}
		recs = pg.Items
	}
	if len(recs) > n {
		recs = recs[len(recs)-n:]
	}
	// newest first
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	return recs, nil
}

type objectData struct {
	base
	Obj       object
	Killed    bool
	Tombstone *tombstone
	Up        []object // lineage root → parent
	Down      []object // this object and its descendants as a tree
	Names     map[string]string
	Cert      *nodeCert
	CertErr   string
	Audit     []auditRecord
	Chains    []chainStatus
	Verified  bool
	Children  int
}

func (u *ui) object(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := objectData{base: u.base(r, "object "+id), Names: map[string]string{}}
	err := u.call(r, "GET", "/v1/objects/"+url.PathEscape(id), &d.Obj)
	var ae *apiError
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.Status == 410 && ae.Tombstone != nil:
		// Killed: the 410 body is the kill record; the listing still has
		// the object's own fields.
		d.Killed, d.Tombstone = true, ae.Tombstone
		objs, _ := listAll[object](u, r, "/v1/objects?state=killed&tenant="+url.QueryEscape(ae.Tombstone.TenantID)+"&limit=500", 4000)
		for _, o := range objs {
			if o.ObjectID == id {
				d.Obj = o
			}
		}
		if d.Obj.ObjectID == "" {
			d.Obj = object{ObjectID: id, TenantID: ae.Tombstone.TenantID, Workload: ae.Tombstone.Workload, Namespace: ae.Tombstone.Namespace,
				ObjectName: ae.Tombstone.ObjectName, Status: "killed", Root: ae.Tombstone.Root}
		}
	default:
		u.fail(w, r, err)
		return
	}
	// Lineage up: the view's lineage (root → self) minus self; killed
	// ancestors answer 410, so resolve names from the root's subtree list.
	root := d.Obj.Root
	if root == "" {
		root = id
	}
	all, _ := listAll[object](u, r, "/v1/objects?root="+url.PathEscape(root)+"&limit=500", 4000)
	byID := map[string]object{}
	for _, o := range all {
		byID[o.ObjectID] = o
		d.Names[o.ObjectID] = o.NodeName()
	}
	for _, lid := range d.Obj.Lineage {
		if lid == id {
			continue
		}
		if o, ok := byID[lid]; ok {
			d.Up = append(d.Up, o)
		} else {
			d.Up = append(d.Up, object{ObjectID: lid, Status: "unknown"})
		}
	}
	if len(d.Up) == 0 && d.Obj.Parent != "" {
		for cur := d.Obj; cur.Parent != "" && len(d.Up) < 16; {
			p, ok := byID[cur.Parent]
			if !ok {
				d.Up = append([]object{{ObjectID: cur.Parent, Status: "unknown"}}, d.Up...)
				break
			}
			d.Up = append([]object{p}, d.Up...)
			cur = p
		}
	}
	if _, ok := byID[id]; !ok {
		all = append(all, d.Obj)
	}
	d.Down = subtree(all, id)
	d.Children = len(d.Down) - 1
	if d.Obj.Live() && d.Obj.StableNodeID != "" {
		var nc nodeCert
		if err := u.call(r, "GET", "/v1/objects/"+url.PathEscape(id)+"/certificate", &nc); err == nil {
			d.Cert = &nc
		} else {
			d.CertErr = err.Error()
		}
	}
	var ap auditPage
	if err := u.call(r, "GET", "/v1/audit?limit=200&object="+url.QueryEscape(id), &ap); err == nil {
		d.Audit, d.Chains, d.Verified = ap.Items, ap.Chains, ap.Verified
	}
	u.render(w, 200, "object", d)
}

type deploymentData struct {
	base
	Dep     deployment
	Objects []object
	Names   map[string]string
	Tenant  tenant
}

func (u *ui) deployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := deploymentData{base: u.base(r, "deployment "+id), Names: map[string]string{}}
	if err := u.call(r, "GET", "/v1/deployments/"+url.PathEscape(id), &d.Dep); err != nil {
		u.fail(w, r, err)
		return
	}
	u.call(r, "GET", "/v1/tenants/"+url.PathEscape(d.Dep.TenantID), &d.Tenant)
	objs, _ := listAll[object](u, r, "/v1/objects?limit=500&tenant="+url.QueryEscape(d.Dep.TenantID)+"&workload="+url.QueryEscape(d.Dep.Workload), 4000)
	for _, o := range objs {
		d.Names[o.ObjectID] = o.NodeName()
	}
	d.Objects = buildTree(objs)
	u.render(w, 200, "deployment", d)
}

type hostData struct {
	base
	Host        host
	Assignments []assignment
	Objects     []object
	Names       map[string]string
}

func (u *ui) host(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := hostData{base: u.base(r, "host "+id), Names: map[string]string{}}
	if err := u.call(r, "GET", "/v1/hosts/"+url.PathEscape(id), &d.Host); err != nil {
		u.fail(w, r, err)
		return
	}
	d.Assignments, _ = listAll[assignment](u, r, "/v1/hosts/"+url.PathEscape(id)+"/assignments?limit=500", 2000)
	// Objects on this host: those of the workloads whose lease it holds.
	for _, a := range d.Assignments {
		objs, _ := listAll[object](u, r, "/v1/objects?limit=500&tenant="+url.QueryEscape(a.TenantID)+"&workload="+url.QueryEscape(a.Workload), 4000)
		for _, o := range objs {
			if o.Live() {
				d.Names[o.ObjectID] = o.NodeName()
				d.Objects = append(d.Objects, o)
			}
		}
	}
	d.Objects = buildTree(d.Objects)
	u.render(w, 200, "host", d)
}

type tenantData struct {
	base
	Tenant      tenant
	Deployments []deployment
	Objects     []object
	Names       map[string]string
	Live        int
	Counts      map[string]int
}

func (u *ui) tenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := tenantData{base: u.base(r, "tenant "+id), Names: map[string]string{}, Counts: map[string]int{}}
	if err := u.call(r, "GET", "/v1/tenants/"+url.PathEscape(id), &d.Tenant); err != nil {
		u.fail(w, r, err)
		return
	}
	d.Deployments, _ = listAll[deployment](u, r, "/v1/deployments?latest=true&limit=500&tenant="+url.QueryEscape(id), 2000)
	objs, _ := listAll[object](u, r, "/v1/objects?limit=500&tenant="+url.QueryEscape(id), 4000)
	for _, o := range objs {
		d.Names[o.ObjectID] = o.NodeName()
		d.Counts[o.Status]++
		if o.Live() {
			d.Live++
		}
	}
	d.Objects = buildTree(objs)
	u.render(w, 200, "tenant", d)
}

type auditData struct {
	base
	Filter       string // object | human | trace | all
	Value        string
	Limit        int
	Records      []auditRecord
	Names        map[string]string
	NextCursor   string
	NextURL      string
	Chains       []chainStatus
	Verified     bool
	Verification verification
}

func (u *ui) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := auditData{base: u.base(r, "audit"), Filter: "all", Limit: 100, Names: map[string]string{}}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		d.Limit = n
	}
	api := url.Values{"limit": {strconv.Itoa(d.Limit)}}
	for _, f := range []string{"object", "human", "trace"} {
		if v := q.Get(f); v != "" {
			d.Filter, d.Value = f, v
			api.Set(f, v)
			break
		}
	}
	if c := q.Get("cursor"); c != "" {
		api.Set("cursor", c)
	}
	var ap auditPage
	if err := u.call(r, "GET", "/v1/audit?"+api.Encode(), &ap); err != nil {
		u.fail(w, r, err)
		return
	}
	d.Records, d.Chains, d.Verified = ap.Items, ap.Chains, ap.Verified
	if ap.NextCursor != nil && *ap.NextCursor != "" {
		d.NextCursor = *ap.NextCursor
		nq := url.Values{"limit": {strconv.Itoa(d.Limit)}, "cursor": {d.NextCursor}}
		if d.Filter != "all" {
			nq.Set(d.Filter, d.Value)
		}
		d.NextURL = "/ui/audit?" + nq.Encode()
	}
	u.call(r, "GET", "/v1/audit/verification", &d.Verification)
	// Node names for the records' objects (one listing; best effort).
	if objs, err := listAll[object](u, r, "/v1/objects?limit=500", 2000); err == nil {
		for _, o := range objs {
			d.Names[o.ObjectID] = o.NodeName()
		}
	}
	u.render(w, 200, "audit", d)
}

// ---- no-JS form fallbacks: the same /v1 call, then a redirect back ----

// mutate performs method path against the API and redirects to back with the
// outcome in ?ok= / ?err= (rendered by the layout as a flash). Browser
// origin checks already ran in mgmt's browser guard before this handler.
func (u *ui) mutate(w http.ResponseWriter, r *http.Request, method, p, back, okMsg string) {
	var res json.RawMessage
	err := u.call(r, method, p, &res)
	to, _ := url.Parse(back)
	q := to.Query()
	if err != nil {
		q.Set("err", err.Error())
	} else {
		q.Set("ok", okMsg)
	}
	to.RawQuery = q.Encode()
	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

func (u *ui) killForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.ParseForm()
	q := url.Values{}
	if r.PostForm.Get("cascade") != "" {
		q.Set("cascade", "true")
	}
	if reason := strings.TrimSpace(r.PostForm.Get("reason")); reason != "" {
		q.Set("reason", reason)
	}
	u.mutate(w, r, "DELETE", "/v1/objects/"+url.PathEscape(id)+"?"+q.Encode(), "/ui/objects/"+url.PathEscape(id), "kill accepted")
}

func (u *ui) undeployForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.ParseForm()
	q := url.Values{}
	if reason := strings.TrimSpace(r.PostForm.Get("reason")); reason != "" {
		q.Set("reason", reason)
	}
	p := "/v1/deployments/" + url.PathEscape(id)
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	u.mutate(w, r, "DELETE", p, "/ui/deployments/"+url.PathEscape(id), "undeploy accepted")
}

func (u *ui) removeHostForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.ParseForm()
	p := "/v1/hosts/" + url.PathEscape(id)
	if r.PostForm.Get("drain") != "" {
		p += "?drain=true"
	}
	u.mutate(w, r, "DELETE", p, "/ui/", "host "+id+" removed")
}

func (u *ui) deleteTenantForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.ParseForm()
	p := "/v1/tenants/" + url.PathEscape(id)
	if r.PostForm.Get("force") != "" {
		p += "?force=true"
	}
	u.mutate(w, r, "DELETE", p, "/ui/", "tenant "+id+" deleted")
}
