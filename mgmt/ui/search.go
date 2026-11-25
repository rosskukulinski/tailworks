package ui

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Search, filters and paging (issue #50): /ui/objects lists objects with
// filters, and /ui/audit combines the /v1/audit filters, newest first.
//
// Every filter is applied by /v1 (issue #512): objects by tenant, workload,
// status (state), tag and human (actingFor); audit records by object,
// human, trace, tenant, workload and event kind, newest first (order=desc)
// with nextCursor paging. /v1/objects has no newest-first order, so the
// objects page reads up to objectsScanMax matches, sorts and pages them
// here, and says when it hit the cap.

// objectsScanMax bounds the objects the UI reads for one listing.
const objectsScanMax = 5000

// objectsPerPage is the UI's page of objects.
const objectsPerPage = 50

type objectsData struct {
	base
	Tenant, Workload, State, Tag, Human string
	Tenants                             []tenant
	States                              []string
	Objects                             []object
	Total                               int  // matches before paging
	Truncated                           bool // objectsScanMax reached
	Page                                int  // 1-based
	PrevURL, NextURL                    string
	Errors                              []string
}

func (u *ui) objects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := objectsData{base: u.base(r, "objects"), Tenant: q.Get("tenant"), Workload: q.Get("workload"), State: q.Get("status"),
		Tag: strings.TrimSpace(q.Get("tag")), Human: strings.TrimSpace(q.Get("human")), Page: 1}
	d.States, _ = uiSpecEnums() // enums.go
	if d.Human == "" {          // the key's name before issue #449, so old links still filter
		d.Human = strings.TrimSpace(q.Get("caller"))
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		d.Page = n
	}
	d.Tenants, _ = listAll[tenant](u, r, "/v1/tenants?limit=500", 2000)
	api := url.Values{"limit": {"500"}}
	if d.Tenant != "" {
		api.Set("tenant", d.Tenant)
	}
	if d.Workload != "" {
		api.Set("workload", d.Workload)
	}
	if d.State != "" {
		api.Set("state", d.State)
	}
	if tag := d.Tag; tag != "" {
		if !strings.HasPrefix(tag, "tag:") {
			tag = "tag:" + tag
		}
		api.Set("tag", tag)
	}
	if d.Human != "" {
		api.Set("human", d.Human)
	}
	match, err := listAll[object](u, r, "/v1/objects?"+api.Encode(), objectsScanMax)
	if err != nil {
		d.Errors = append(d.Errors, "objects: "+err.Error())
	}
	d.Truncated = len(match) >= objectsScanMax
	// Newest first, like every activity list in the UI.
	sort.SliceStable(match, func(i, j int) bool { return match[i].CreatedAt > match[j].CreatedAt })
	d.Total = len(match)
	start := (d.Page - 1) * objectsPerPage
	if start > len(match) {
		start = len(match)
	}
	end := min(start+objectsPerPage, len(match))
	d.Objects = match[start:end]
	link := func(p int) string {
		v := url.Values{}
		for _, k := range []string{"tenant", "workload", "status", "tag"} {
			if s := q.Get(k); s != "" {
				v.Set(k, s)
			}
		}
		if d.Human != "" {
			v.Set("human", d.Human)
		}
		if p > 1 {
			v.Set("page", strconv.Itoa(p))
		}
		if len(v) == 0 {
			return "/ui/objects"
		}
		return "/ui/objects?" + v.Encode()
	}
	if d.Page > 1 {
		d.PrevURL = link(d.Page - 1)
	}
	if end < len(match) {
		d.NextURL = link(d.Page + 1)
	}
	u.render(w, 200, "objects", d)
}

// FirstShown and LastShown number the rows on this page (1-based).
func (d objectsData) FirstShown() int {
	if len(d.Objects) == 0 {
		return 0
	}
	return (d.Page-1)*objectsPerPage + 1
}
func (d objectsData) LastShown() int { return (d.Page-1)*objectsPerPage + len(d.Objects) }

// ---- audit ----

type auditData struct {
	base
	Object, Human, Trace, Tenant, Kind, Workload string
	Limit                                        int
	Records                                      []auditRecord
	Names                                        map[string]string
	NextCursor                                   string
	NextURL, FirstURL                            string
	Paged                                        bool // not the first page
	Chains                                       []chainStatus
	Verified                                     bool
	Verification                                 verification
	Tenants                                      []tenant
}

// Filtered: any filter is set.
func (d auditData) Filtered() bool {
	return d.Object != "" || d.Human != "" || d.Trace != "" || d.Tenant != "" || d.Kind != "" || d.Workload != ""
}

func (u *ui) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := auditData{base: u.base(r, "audit"), Limit: 100, Names: map[string]string{},
		Object: q.Get("object"), Human: q.Get("human"), Trace: q.Get("trace"), Tenant: q.Get("tenant"), Kind: q.Get("kind"),
		Workload: strings.TrimSpace(q.Get("workload"))}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		d.Limit = n
	}
	filters := url.Values{}
	for k, v := range map[string]string{"object": d.Object, "human": d.Human, "trace": d.Trace, "tenant": d.Tenant, "kind": d.Kind, "workload": d.Workload} {
		if v != "" {
			filters.Set(k, v)
		}
	}
	api := url.Values{"limit": {strconv.Itoa(d.Limit)}, "order": {"desc"}}
	for k, v := range filters {
		api[k] = v
	}
	if c := q.Get("cursor"); c != "" {
		api.Set("cursor", c)
		d.Paged = true
	}
	var ap auditPage
	if err := u.call(r, "GET", "/v1/audit?"+api.Encode(), &ap); err != nil {
		u.fail(w, r, err)
		return
	}
	d.Chains, d.Verified = ap.Chains, ap.Verified
	ui := url.Values{"limit": {strconv.Itoa(d.Limit)}}
	for k, v := range filters {
		ui[k] = v
	}
	d.FirstURL = "/ui/audit?" + ui.Encode()
	if ap.NextCursor != nil && *ap.NextCursor != "" {
		d.NextCursor = *ap.NextCursor
		ui.Set("cursor", d.NextCursor)
		d.NextURL = "/ui/audit?" + ui.Encode()
	}
	u.call(r, "GET", "/v1/audit/verification", &d.Verification)
	d.Tenants, _ = listAll[tenant](u, r, "/v1/tenants?limit=500", 2000)
	// Node names for the records' objects (best effort).
	if objs, err := listAll[object](u, r, "/v1/objects?limit=500", 2000); err == nil {
		for _, o := range objs {
			d.Names[o.ObjectID] = o.NodeName()
		}
	}
	d.Records = ap.Items
	u.render(w, 200, "audit", d)
}

// AuditKinds are the /v1 audit kinds, for the event filter's suggestions.
func (d auditData) AuditKinds() []string {
	_, kinds := uiSpecEnums() // enums.go
	return kinds
}
