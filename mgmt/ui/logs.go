package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Logs: a workload's logs (/ui/tenants/{id}/workloads/{workload}/logs) and
// a node's (/ui/objects/{id}/logs), both read with GET
// /v1/tenants/{id}/workloads/{workload}/logs (ADR-045).
//
//   - source=requests (the default): the structured request records, newest
//     first, each with its console.* lines and exceptions. A node's page is
//     the workload's records filtered to that object by the host
//     (`object=<id>`).
//   - source=console: the workload's raw output (workerd's stdout, the node
//     tier's app.log, the microVM console), in file order. It is per
//     workload, not per node.
//
// The page reads the last n records or lines; app.js re-fetches it every
// 5 s like every other page, so it follows the log without a stream.

// logsDefaultN and logSizes: how many records or lines a page shows.
const logsDefaultN = 100

var logSizes = []int{
	50,
	100,
	200,
	500,
}

// logRecord is a RequestRecord (api/openapi.yaml) as the UI shows it.
type logRecord struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Host       string    `json:"host"`
	Workload   string    `json:"workload"`
	Deployment string    `json:"deployment"`
	Object     string    `json:"object"`
	Class      string    `json:"class"`
	Name       string    `json:"name"`
	Caller     *struct {
		Node   string `json:"node"`
		NodeID string `json:"nodeId"`
		User   string `json:"user"`
		Object string `json:"object"`
		Public bool   `json:"public"`
		IP     string `json:"ip"`
	} `json:"caller"`
	Method       string  `json:"method"`
	Path         string  `json:"path"`
	Status       int     `json:"status"`
	DurationMs   float64 `json:"durationMs"`
	Event        string  `json:"event"`
	Outcome      string  `json:"outcome"`
	TraceID      string  `json:"traceId"`
	AuditTraceID string  `json:"auditTraceId"`
	Logs         []struct {
		Time    time.Time `json:"time"`
		Level   string    `json:"level"`
		Message string    `json:"message"`
	} `json:"logs"`
	Exceptions []struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		Stack   string `json:"stack"`
	} `json:"exceptions"`
	Truncated bool `json:"truncated"`
	Dropped   int  `json:"dropped"`

	// From the replica prefix ("[<host>] ") when the workload has several.
	Replica string `json:"-"`
	// The object's node name, from the workload's objects.
	NodeName string `json:"-"`
}

// Clock is the record's time of day (UTC, milliseconds); Stamp the full time.
func (r logRecord) Clock() string { return r.Time.UTC().Format("15:04:05.000") }
func (r logRecord) Stamp() string { return r.Time.UTC().Format(time.RFC3339Nano) }

// IsError matches the API's errors filter: status ≥ 400, an outcome other
// than ok, or an exception.
func (r logRecord) IsError() bool {
	return r.Status >= 400 || (r.Outcome != "" && r.Outcome != "ok") || len(r.Exceptions) > 0
}

// StatusClass is the badge class of the record's status.
func (r logRecord) StatusClass() string {
	switch {
	case r.Status >= 500:
		return "bad"
	case r.Status >= 400:
		return "warn"
	case r.Status >= 200 && r.Status < 300:
		return "ok"
	}
	return ""
}

// Duration is durationMs for display.
func (r logRecord) Duration() string {
	switch {
	case r.DurationMs <= 0:
		return ""
	case r.DurationMs < 10:
		return strconv.FormatFloat(r.DurationMs, 'f', 1, 64) + " ms"
	case r.DurationMs < 10000:
		return strconv.FormatFloat(r.DurationMs, 'f', 0, 64) + " ms"
	}
	return strconv.FormatFloat(r.DurationMs/1000, 'f', 1, 64) + " s"
}

// What is the record's one-line summary: the request, or the invocation.
func (r logRecord) What() string {
	switch {
	case r.Kind == "dropped":
		return fmt.Sprintf("%d records dropped (rate limit)", r.Dropped)
	case r.Method != "" || r.Path != "":
		return strings.TrimSpace(r.Method + " " + r.Path)
	case r.Event != "":
		return r.Event
	}
	return r.Kind
}

// Who is the verified caller, short.
func (r logRecord) Who() string {
	c := r.Caller
	switch {
	case c == nil:
		return ""
	case c.Public:
		if c.IP != "" {
			return "public:" + c.IP
		}
		return "public"
	case c.User != "":
		return c.User
	case c.Node != "":
		h, _, _ := strings.Cut(c.Node, ".")
		return h
	}
	return c.NodeID
}

// Where is the record's object for display: its node name, the DO it is,
// or the object ID.
func (r logRecord) Where() string {
	switch {
	case r.NodeName != "":
		return r.NodeName
	case r.Class != "" && r.Name != "":
		return r.Class + "/" + r.Name
	case r.Class != "":
		return r.Class
	}
	return short(r.Object)
}

// consoleLine is one line of the raw output.
type consoleLine struct {
	Replica string
	Text    string
}

type logsData struct {
	base
	TenantID, Workload string
	Tenant             tenant
	DeploymentID       string
	Node               *object // the node page's object (nil: the workload's page)

	// The query.
	Source                          string // requests | console
	N                               int
	Sizes                           []int
	Errors                          bool
	StatusQ, Caller, Trace, ObjectQ string

	Records []logRecord
	Lines   []consoleLine
	Notes   []string // a replica's host that did not answer
	Empty   string   // why there is nothing to show (no_logs, unknown_workload)
	Err     string
}

// Filtered reports whether any record filter is set.
func (d logsData) Filtered() bool {
	return d.Errors || d.StatusQ != "" || d.Caller != "" || d.Trace != "" || d.ObjectQ != ""
}

// Self is this page's path.
func (d logsData) Self() string {
	if d.Node != nil {
		return "/ui/objects/" + url.PathEscape(d.Node.ObjectID) + "/logs"
	}
	return "/ui/tenants/" + url.PathEscape(d.TenantID) + "/workloads/" + url.PathEscape(d.Workload) + "/logs"
}

// WorkloadPage is the workload's logs page.
func (d logsData) WorkloadPage() string {
	return "/ui/tenants/" + url.PathEscape(d.TenantID) + "/workloads/" + url.PathEscape(d.Workload) + "/logs"
}

// With is this page with the query changed: pairs of name, value ("" drops
// the parameter). Every link keeps n.
func (d logsData) With(kv ...string) string {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	if d.Source != "requests" {
		q.Set("source", d.Source)
	}
	if d.N != logsDefaultN {
		q.Set("n", strconv.Itoa(d.N))
	}
	if d.Errors {
		q.Set("errors", "1")
	}
	set("status", d.StatusQ)
	set("caller", d.Caller)
	set("trace", d.Trace)
	set("object", d.ObjectQ)
	for i := 0; i+1 < len(kv); i += 2 {
		q.Del(kv[i])
		set(kv[i], kv[i+1])
	}
	if q.Get("source") == "requests" {
		q.Del("source")
	}
	p := d.Self()
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return p
}

// readLogsQuery fills d's query from the page's URL.
func readLogsQuery(d *logsData, r *http.Request) {
	q := r.URL.Query()
	d.Sizes = logSizes
	d.Source = "requests"
	if q.Get("source") == "console" && d.Node == nil {
		d.Source = "console"
	}
	d.N = logsDefaultN
	if n, err := strconv.Atoi(q.Get("n")); err == nil && n > 0 {
		d.N = min(n, logSizes[len(logSizes)-1])
	}
	d.Errors = q.Get("errors") == "1" || q.Get("errors") == "true"
	d.StatusQ = strings.TrimSpace(q.Get("status"))
	d.Caller = strings.TrimSpace(q.Get("caller"))
	d.Trace = strings.TrimSpace(q.Get("trace"))
	if d.Node == nil {
		d.ObjectQ = strings.TrimSpace(q.Get("object"))
	}
}

func (u *ui) workloadLogs(w http.ResponseWriter, r *http.Request) {
	id, wl := r.PathValue("id"), r.PathValue("workload")
	d := logsData{base: u.base(r, wl+" logs"), TenantID: id, Workload: wl}
	if err := u.call(r, "GET", "/v1/tenants/"+url.PathEscape(id), &d.Tenant); err != nil {
		u.fail(w, r, err)
		return
	}
	readLogsQuery(&d, r)
	u.readLogs(r, &d)
	u.render(w, 200, "logs", d)
}

// objectLogs is a node's logs: its workload's records for this object.
func (u *ui) objectLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var o object
	err := u.call(r, "GET", "/v1/objects/"+url.PathEscape(id), &o)
	var ae *apiError
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.Status == 410 && ae.Tombstone != nil:
		// Killed: its records are still in the workload's log.
		t := ae.Tombstone
		o = object{ObjectID: id, TenantID: t.TenantID, Workload: t.Workload, Namespace: t.Namespace, ObjectName: t.ObjectName, Status: "killed"}
	default:
		u.fail(w, r, err)
		return
	}
	d := logsData{base: u.base(r, o.NodeName()+" logs"), TenantID: o.TenantID, Workload: o.Workload, Node: &o}
	u.call(r, "GET", "/v1/tenants/"+url.PathEscape(o.TenantID), &d.Tenant)
	readLogsQuery(&d, r)
	if o.Workload == "" {
		d.Empty = "This node does not belong to a workload, so tailworks keeps no logs for it."
	} else {
		u.readLogs(r, &d)
	}
	u.render(w, 200, "logs", d)
}

// readLogs reads d's query from the API into d.
func (u *ui) readLogs(r *http.Request, d *logsData) {
	if deps, err := listAll[deployment](u, r, "/v1/deployments?latest=true&limit=1&tenant="+url.QueryEscape(d.TenantID)+"&workload="+url.QueryEscape(d.Workload), 0); err == nil && len(deps) > 0 {
		d.DeploymentID = deps[0].DeploymentID
	}
	q := url.Values{"n": {strconv.Itoa(d.N)}, "source": {d.Source}}
	if d.Source == "requests" {
		set := func(k, v string) {
			if v != "" {
				q.Set(k, v)
			}
		}
		if d.Node != nil {
			set("object", d.Node.ObjectID)
		}
		set("object", d.ObjectQ)
		set("status", d.StatusQ)
		set("caller", d.Caller)
		set("trace", d.Trace)
		if d.Errors {
			q.Set("errors", "true")
		}
	}
	accept := "text/plain"
	if d.Source == "requests" {
		accept = "application/x-ndjson"
	}
	body, err := u.fetch(r, "GET", "/v1/tenants/"+url.PathEscape(d.TenantID)+"/workloads/"+url.PathEscape(d.Workload)+"/logs?"+q.Encode(), accept)
	var ae *apiError
	switch {
	case errors.As(err, &ae) && (ae.Code == "no_logs" || ae.Code == "unknown_workload"):
		d.Empty = ae.Message
		return
	case err != nil:
		d.Err = err.Error()
		return
	}
	if d.Source == "console" {
		d.Lines, d.Notes = parseConsole(body)
		return
	}
	d.Records, d.Notes = parseRecords(body)
	if len(d.Records) == 0 {
		return
	}
	names := map[string]string{}
	objs, _ := listAll[object](u, r, "/v1/objects?limit=500&tenant="+url.QueryEscape(d.TenantID)+"&workload="+url.QueryEscape(d.Workload), 4000)
	for _, o := range objs {
		names[o.ObjectID] = o.NodeName()
	}
	if d.Node != nil {
		names[d.Node.ObjectID] = d.Node.NodeName()
	}
	for i := range d.Records {
		d.Records[i].NodeName = names[d.Records[i].Object]
	}
}

// splitReplica takes the "[<host>] " prefix mgmt adds to each line when a
// workload has several replicas.
func splitReplica(line string) (host, rest string) {
	if strings.HasPrefix(line, "[") {
		if i := strings.Index(line, "] "); i > 1 && !strings.ContainsAny(line[1:i], " []") {
			return line[1:i], line[i+2:]
		}
	}
	return "", line
}

// replicaNote is a "[<host>] tw: …" line for display.
func replicaNote(host, line string) string {
	line = strings.TrimPrefix(line, "tw: ")
	if host == "" {
		return line
	}
	return host + ": " + line
}

// parseRecords reads JSON Lines of records, newest first. A "tw: …" line is
// a replica's host that did not answer.
func parseRecords(body []byte) (recs []logRecord, notes []string) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		host, rest := splitReplica(line)
		if strings.HasPrefix(rest, "tw: ") {
			notes = append(notes, replicaNote(host, rest))
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(rest), &rec); err != nil {
			notes = append(notes, "an unreadable record was skipped")
			continue
		}
		rec.Replica = host
		if rec.Replica == "" {
			rec.Replica = rec.Host
		}
		recs = append(recs, rec)
	}
	// Replicas come one after the other; newest first across them.
	slices.SortStableFunc(recs, func(a, b logRecord) int { return b.Time.Compare(a.Time) })
	return recs, notes
}

// ansi matches terminal escape sequences (colours, cursor moves).
var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// parseConsole reads the raw output in file order, without terminal
// escapes and control characters.
func parseConsole(body []byte) (lines []consoleLine, notes []string) {
	text := strings.TrimRight(string(body), "\n")
	if text == "" {
		return nil, nil
	}
	for _, line := range strings.Split(text, "\n") {
		host, rest := splitReplica(strings.TrimRight(line, "\r"))
		if host != "" && strings.HasPrefix(rest, "tw: ") {
			notes = append(notes, replicaNote(host, rest))
			continue
		}
		rest = strings.Map(func(c rune) rune {
			if c < 0x20 && c != '\t' || c == 0x7f {
				return -1
			}
			return c
		}, ansi.ReplaceAllString(rest, ""))
		lines = append(lines, consoleLine{Replica: host, Text: rest})
	}
	return lines, notes
}
