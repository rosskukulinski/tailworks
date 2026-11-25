package main

// tw dev's console (issue #50): a read-only local data explorer, like the
// one `wrangler dev`'s e hotkey opens. It serves on 127.0.0.1:<port>
// (--console-port; 0 picks a free port, -1 turns it off) and shows the
// dev run's own data, read straight from dev's stores, not through the
// /v1 API:
//
//   - Workers KV namespaces and their keys and values, folded from the
//     in-memory mgmt's journals (the same journals devstores.go saves);
//   - D1 databases, their tables and rows, through the dev host's store
//     admin op (the path `tw d1 execute` takes), with SELECTs only.
//
// Nothing here writes. It answers GET only and only for a Host header
// naming the loopback listener (so a web page can't read it through DNS
// rebinding), and sends the admin UI's security headers and stylesheet.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
	"github.com/rosskukulinski/tailworks/internal/kvwire"
	"github.com/rosskukulinski/tailworks/mgmt/ui"
)

const (
	// devConsoleKeysPerPage and devConsoleRowsPerPage bound a list page.
	devConsoleKeysPerPage = 100
	devConsoleRowsPerPage = 50
	// devConsolePreview is how much of a KV value the key list shows.
	devConsolePreview = 120
	// devConsoleValueMax is how much of one value its page shows.
	devConsoleValueMax = 256 << 10
)

// devConsole is the console's data: the workload's stores in the dev run.
type devConsole struct {
	workload string
	appURL   string
	// stores returns the in-memory mgmt's store journals of the dev tenant.
	stores func() []fakemgmt.StoreSnapshot
	// d1 runs a batch on database db of the workload.
	d1  func(ctx context.Context, db string, qs []d1mig.Query) ([]d1mig.Result, error)
	now func() time.Time

	mu  sync.Mutex
	dbs []devD1 // the app's D1 databases, updated on each reload
}

// setD1 records the app's D1 databases after a (re)deploy. Nil-safe.
func (c *devConsole) setD1(dbs []devD1) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.dbs = append([]devD1(nil), dbs...)
	c.mu.Unlock()
}

func (c *devConsole) databases() []devD1 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]devD1(nil), c.dbs...)
}

// newDevConsole is the console of the dev run's workload: its KV from fm's
// journals of tenantID, its D1 through h's store admin op.
func newDevConsole(fm *fakemgmt.Server, h *hostd.Host, tenantID, workload, appURL string, dbs []devD1) *devConsole {
	c := &devConsole{
		workload: workload,
		appURL:   appURL,
		stores:   func() []fakemgmt.StoreSnapshot { return fm.ExportStores(tenantID) },
		d1: func(ctx context.Context, db string, qs []d1mig.Query) ([]d1mig.Result, error) {
			return devD1Exec(h, tenantID, workload, db)(ctx, qs)
		},
		now: time.Now,
	}
	c.setD1(dbs)
	return c
}

// startDevConsole serves c on 127.0.0.1:port (port < 0: off, returns "").
func startDevConsole(port int, c *devConsole) (consoleURL string, stop func(), err error) {
	if port < 0 {
		return "", func() {}, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", func() {}, fail("bad_request", fmt.Sprintf("can't listen on 127.0.0.1:%d for the dev console", port), err.Error()).
			withFix(fmt.Sprintf("tw dev --console-port %d    (or -1 for no console)", port+1))
	}
	srv := &http.Server{Handler: c.handler(ln.Addr().(*net.TCPAddr).Port), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String() + "/", func() { srv.Close() }, nil
}

// handler serves the console for a listener on 127.0.0.1:port.
func (c *devConsole) handler(port int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", c.overview)
	mux.HandleFunc("GET /kv/{name}", c.kvKeys)
	mux.HandleFunc("GET /kv/{name}/value", c.kvValue)
	mux.HandleFunc("GET /d1/{db}", c.d1Tables)
	mux.HandleFunc("GET /d1/{db}/{table}", c.d1Rows)
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.StaticHandler()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "the dev console is read-only", http.StatusMethodNotAllowed)
			return
		}
		c.render(w, http.StatusNotFound, "Not found", nil, []string{"No such page."}, nil)
	})
	hosts := map[string]bool{}
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]"} {
		hosts[h+":"+strconv.Itoa(port)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src data:; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cache-Control", "no-store")
		if !hosts[r.Host] {
			http.Error(w, "tw dev console: open it as http://127.0.0.1:"+strconv.Itoa(port)+"/", http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- data

// devKVNamespace is one KV store of the workload, folded.
type devKVNamespace struct {
	Name  string
	Items map[string]kvwire.Item
	Err   string
}

// kvNamespaces folds the workload's KV journals, sorted by name.
func (c *devConsole) kvNamespaces() []devKVNamespace {
	var out []devKVNamespace
	for _, sn := range c.stores() {
		if sn.Kind != kvwire.Kind || sn.Workload != c.workload {
			continue
		}
		es := append([]fakemgmt.StoreSnapshotEntry(nil), sn.Entries...)
		sort.Slice(es, func(i, j int) bool { return es[i].Seq < es[j].Seq })
		data := make([][]byte, len(es))
		for i, e := range es {
			data[i] = e.Data
		}
		ns := devKVNamespace{Name: sn.Name}
		items, err := kvwire.Fold(data, c.now().Unix())
		if err != nil {
			ns.Err = err.Error()
		}
		ns.Items = items
		out = append(out, ns)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *devConsole) kvNamespace(name string) (devKVNamespace, bool) {
	for _, ns := range c.kvNamespaces() {
		if ns.Name == name {
			return ns, true
		}
	}
	return devKVNamespace{}, false
}

// devOtherStore is a store the console lists but can't browse (R2, queues).
type devOtherStore struct {
	Kind, Name string
	Entries    int
}

func (c *devConsole) otherStores() []devOtherStore {
	var out []devOtherStore
	for _, sn := range c.stores() {
		if sn.Workload != c.workload || sn.Kind == kvwire.Kind || sn.Kind == "d1" {
			continue
		}
		out = append(out, devOtherStore{Kind: sn.Kind, Name: sn.Name, Entries: len(sn.Entries)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (c *devConsole) database(name string) (devD1, bool) {
	for _, d := range c.databases() {
		if d.Database == name {
			return d, true
		}
	}
	return devD1{}, false
}

// devD1Table is one table of a D1 database.
type devD1Table struct {
	Name string
	Rows int64
}

// d1TablesQuery lists a database's own tables (not SQLite's or D1's).
const d1TablesQuery = `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name NOT LIKE '\_cf\_%' ESCAPE '\' ORDER BY name`

// listD1Tables lists db's tables with their row counts.
func (c *devConsole) listD1Tables(ctx context.Context, db string) ([]devD1Table, error) {
	rs, err := c.d1(ctx, db, []d1mig.Query{{SQL: d1TablesQuery}})
	if err != nil {
		return nil, err
	}
	if len(rs) != 1 {
		return nil, fmt.Errorf("D1 answered %d results for 1 query", len(rs))
	}
	var tables []devD1Table
	var counts []d1mig.Query
	for _, row := range rs[0].Results.Rows {
		if len(row) == 0 {
			continue
		}
		n, _ := row[0].(string)
		tables = append(tables, devD1Table{Name: n})
		counts = append(counts, d1mig.Query{SQL: "SELECT COUNT(*) FROM " + sqlIdent(n)})
	}
	if len(counts) == 0 {
		return nil, nil
	}
	cs, err := c.d1(ctx, db, counts)
	if err != nil {
		return nil, err
	}
	for i := range tables {
		if i < len(cs) && len(cs[i].Results.Rows) == 1 && len(cs[i].Results.Rows[0]) == 1 {
			if f, ok := cs[i].Results.Rows[0][0].(float64); ok {
				tables[i].Rows = int64(f)
			}
		}
	}
	return tables, nil
}

// sqlIdent quotes a SQLite identifier.
func sqlIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// ---------------------------------------------------------------- pages

var devConsoleTemplate = template.Must(template.New("console").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>tw dev · {{.Title}}</title>
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
<header class="top">
  <a class="brand" href="/">tw dev</a>
  <nav><a href="/">Data</a> <a href="{{.AppURL}}" rel="noreferrer">Open the app</a></nav>
  <div class="who">Workload <span class="mono">{{.Workload}}</span> · read-only</div>
</header>
<main>
{{range .Errors}}<div class="flash bad" role="alert">{{.}}</div>{{end}}
{{.Body}}
</main>
<footer><span>Local data of this tw dev run. Reload to refresh.</span></footer>
</body>
</html>`))

// devPage is what every console page passes its template.
type devPage struct {
	Title, Workload, AppURL string
	Errors                  []string
	Body                    template.HTML
}

// render writes one page: body is the named content template run on data.
func (c *devConsole) render(w http.ResponseWriter, status int, title string, body *template.Template, errs []string, data any) {
	p := devPage{Title: title, Workload: c.workload, AppURL: c.appURL, Errors: errs}
	if body != nil {
		var b strings.Builder
		if err := body.Execute(&b, data); err != nil {
			p.Errors = append(p.Errors, err.Error())
		}
		p.Body = template.HTML(b.String()) // body is html/template output
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	devConsoleTemplate.Execute(w, p)
}

func devBody(src string) *template.Template {
	return template.Must(template.New("body").Funcs(template.FuncMap{
		"q":     url.PathEscape,
		"query": url.QueryEscape,
		"expires": func(x int64) string {
			if x == 0 {
				return "never"
			}
			return time.Unix(x, 0).UTC().Format(time.RFC3339)
		},
	}).Parse(src))
}

var devOverviewBody = devBody(`<p class="hint">The data this <span class="mono">tw dev</span> run holds. KV namespaces are kept between runs in <span class="mono">.tailworks/dev-stores.json</span>; D1 databases start empty each run and get the app's migrations.</p>
<section class="card">
  <div class="sect-head"><div><h2>Workers KV <span class="count">{{len .KV}}</span></h2></div></div>
  <div class="tbl"><table>
  <thead><tr><th>Namespace</th><th>Keys</th></tr></thead>
  <tbody>{{range .KV}}<tr><td class="mono"><a href="/kv/{{q .Name}}">{{.Name}}</a></td><td>{{len .Items}}</td></tr>{{else}}<tr><td colspan="2" class="empty">No KV namespace holds data yet.</td></tr>{{end}}</tbody>
  </table></div>
</section>
<section class="card">
  <div class="sect-head"><div><h2>D1 databases <span class="count">{{len .D1}}</span></h2></div></div>
  <div class="tbl"><table>
  <thead><tr><th>Database</th><th>Binding</th><th>Migrations applied this run</th></tr></thead>
  <tbody>{{range .D1}}<tr><td class="mono"><a href="/d1/{{q .Database}}">{{.Database}}</a></td><td class="mono">{{.Binding}}</td><td>{{len .Applied}}</td></tr>{{else}}<tr><td colspan="3" class="empty">The app binds no D1 database.</td></tr>{{end}}</tbody>
  </table></div>
</section>
{{if .Other}}<section class="card">
  <div class="sect-head"><div><h2>Other stores <span class="count">{{len .Other}}</span></h2><p class="hint">Listed, not browsable here.</p></div></div>
  <div class="tbl"><table>
  <thead><tr><th>Kind</th><th>Name</th><th>Journal entries</th></tr></thead>
  <tbody>{{range .Other}}<tr><td>{{.Kind}}</td><td class="mono">{{.Name}}</td><td>{{.Entries}}</td></tr>{{end}}</tbody>
  </table></div>
</section>{{end}}`)

func (c *devConsole) overview(w http.ResponseWriter, r *http.Request) {
	c.render(w, 200, "Data", devOverviewBody, nil, map[string]any{
		"KV":    c.kvNamespaces(),
		"D1":    c.databases(),
		"Other": c.otherStores(),
	})
}

// devKVRow is one key in the key list.
type devKVRow struct {
	Key, Preview, Metadata string
	Bytes                  int
	Expiration             int64
	Binary                 bool
}

var devKVKeysBody = devBody(`<p class="hint"><a href="/">Data</a> / KV namespace <span class="mono">{{.Name}}</span></p>
<section class="card toolbar">
  <form method="get" action="/kv/{{q .Name}}" class="filters">
    <label>Key prefix <input type="text" name="prefix" class="mono" value="{{.Prefix}}" placeholder="any"></label>
    <button type="submit">Filter</button>
  </form>
</section>
<section class="card">
  <div class="sect-head"><div><h2>Keys <span class="count">{{.Total}}</span></h2><p class="hint">Sorted by key, as <span class="mono">list()</span> returns them; expired keys are left out.</p></div></div>
  <div class="tbl"><table>
  <thead><tr><th>Key</th><th>Value</th><th>Size</th><th>Expires</th><th>Metadata</th></tr></thead>
  <tbody>{{range .Rows}}<tr>
    <td class="mono"><a href="/kv/{{q $.Name}}/value?key={{query .Key}}">{{.Key}}</a></td>
    <td class="mono small">{{if .Binary}}<span class="muted">binary</span>{{else}}{{.Preview}}{{end}}</td>
    <td class="small">{{.Bytes}} B</td>
    <td class="small">{{expires .Expiration}}</td>
    <td class="mono small">{{if .Metadata}}{{.Metadata}}{{else}}<span class="muted">—</span>{{end}}</td>
  </tr>{{else}}<tr><td colspan="5" class="empty">No keys{{if .Prefix}} start with that prefix{{end}}.</td></tr>{{end}}</tbody>
  </table></div>
  <p class="pager">{{if .Cursor}}<a href="/kv/{{q .Name}}?prefix={{query .Prefix}}">← First</a>{{end}}{{if .Next}}<a href="/kv/{{q .Name}}?prefix={{query .Prefix}}&amp;cursor={{query .Next}}">Next →</a>{{end}}</p>
</section>`)

func (c *devConsole) kvKeys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns, ok := c.kvNamespace(name)
	if !ok {
		c.render(w, 404, "KV", nil, []string{"No KV namespace " + strconv.Quote(name) + " holds data in this run."}, nil)
		return
	}
	prefix, cursor := r.URL.Query().Get("prefix"), r.URL.Query().Get("cursor")
	all, _ := kvwire.Page(ns.Items, prefix, "", kvwire.MaxListKeys)
	page, next := devKVPage(ns.Items, prefix, cursor)
	rows := make([]devKVRow, 0, len(page))
	for _, it := range page {
		row := devKVRow{Key: it.Key, Metadata: it.Metadata, Bytes: len(it.Value), Expiration: it.Expiration}
		if utf8.Valid(it.Value) {
			row.Preview = truncateRunes(string(it.Value), devConsolePreview)
		} else {
			row.Binary = true
		}
		rows = append(rows, row)
	}
	total := strconv.Itoa(len(all))
	if len(all) == kvwire.MaxListKeys {
		total += "+"
	}
	var errs []string
	if ns.Err != "" {
		errs = append(errs, "the namespace's journal has an entry tailworks can't read: "+ns.Err)
	}
	c.render(w, 200, "KV "+name, devKVKeysBody, errs, map[string]any{
		"Name": name, "Prefix": prefix, "Cursor": cursor, "Next": next, "Rows": rows, "Total": total,
	})
}

// devKVPage is one page of keys after cursor.
func devKVPage(items map[string]kvwire.Item, prefix, cursor string) ([]kvwire.Item, string) {
	return kvwire.Page(items, prefix, cursor, devConsoleKeysPerPage)
}

var devKVValueBody = devBody(`<p class="hint"><a href="/">Data</a> / KV namespace <a href="/kv/{{q .Name}}" class="mono">{{.Name}}</a> / <span class="mono">{{.Key}}</span></p>
<section class="card">
  <dl class="facts">
    <dt>Size</dt><dd>{{.Bytes}} B</dd>
    <dt>Expires</dt><dd>{{expires .Expiration}}</dd>
    <dt>Metadata</dt><dd class="mono">{{if .Metadata}}{{.Metadata}}{{else}}—{{end}}</dd>
  </dl>
  {{if .Truncated}}<p class="hint">Showing the first {{.Shown}} bytes.</p>{{end}}
  {{if .Binary}}<p class="hint">Not UTF-8 text; shown as hex.</p>{{end}}
  <pre class="mono">{{.Value}}</pre>
</section>`)

func (c *devConsole) kvValue(w http.ResponseWriter, r *http.Request) {
	name, key := r.PathValue("name"), r.URL.Query().Get("key")
	ns, ok := c.kvNamespace(name)
	it, found := ns.Items[key]
	if !ok || !found {
		c.render(w, 404, "KV", nil, []string{"No key " + strconv.Quote(key) + " in KV namespace " + strconv.Quote(name) + "."}, nil)
		return
	}
	v, truncated := it.Value, false
	if len(v) > devConsoleValueMax {
		v, truncated = v[:devConsoleValueMax], true
	}
	shown, binary := string(v), !utf8.Valid(v)
	if binary {
		shown = hex.Dump(v)
	} else if pretty, ok := prettyJSON(v); ok {
		shown = pretty
	}
	c.render(w, 200, "KV "+key, devKVValueBody, nil, map[string]any{
		"Name": name, "Key": key, "Bytes": len(it.Value), "Expiration": it.Expiration, "Metadata": it.Metadata,
		"Value": shown, "Truncated": truncated, "Shown": len(v), "Binary": binary,
	})
}

// prettyJSON indents v when it is a JSON object or array.
func prettyJSON(v []byte) (string, bool) {
	t := strings.TrimSpace(string(v))
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return "", false
	}
	var x any
	if json.Unmarshal(v, &x) != nil {
		return "", false
	}
	b, err := json.MarshalIndent(x, "", "  ")
	return string(b), err == nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

var devD1TablesBody = devBody(`<p class="hint"><a href="/">Data</a> / D1 database <span class="mono">{{.DB.Database}}</span> (binding <span class="mono">{{.DB.Binding}}</span>)</p>
<section class="card">
  <div class="sect-head"><div><h2>Tables <span class="count">{{len .Tables}}</span></h2></div></div>
  <div class="tbl"><table>
  <thead><tr><th>Table</th><th>Rows</th></tr></thead>
  <tbody>{{range .Tables}}<tr><td class="mono"><a href="/d1/{{q $.DB.Database}}/{{q .Name}}">{{.Name}}</a></td><td>{{.Rows}}</td></tr>{{else}}<tr><td colspan="2" class="empty">No tables yet.</td></tr>{{end}}</tbody>
  </table></div>
</section>`)

func (c *devConsole) d1Tables(w http.ResponseWriter, r *http.Request) {
	db, ok := c.database(r.PathValue("db"))
	if !ok {
		c.render(w, 404, "D1", nil, []string{"The app binds no D1 database " + strconv.Quote(r.PathValue("db")) + "."}, nil)
		return
	}
	tables, err := c.listD1Tables(r.Context(), db.Database)
	var errs []string
	if err != nil {
		errs = append(errs, "D1: "+err.Error())
	}
	c.render(w, 200, "D1 "+db.Database, devD1TablesBody, errs, map[string]any{"DB": db, "Tables": tables})
}

var devD1RowsBody = devBody(`<p class="hint"><a href="/">Data</a> / D1 database <a href="/d1/{{q .DB}}" class="mono">{{.DB}}</a> / table <span class="mono">{{.Table}}</span></p>
<section class="card">
  <div class="sect-head"><div><h2>Rows</h2><p class="hint">In rowid order{{if .Rows}}: rows {{.First}}–{{.Last}}{{end}}. Run your own SQL with <span class="mono">tw d1 execute</span> against a deployed app.</p></div></div>
  <div class="tbl"><table>
  <thead><tr>{{range .Columns}}<th class="mono">{{.}}</th>{{end}}</tr></thead>
  <tbody>{{range .Rows}}<tr>{{range .}}<td class="mono small">{{if .Null}}<span class="muted">NULL</span>{{else}}{{.Text}}{{end}}</td>{{end}}</tr>{{else}}<tr><td colspan="{{len .Columns}}" class="empty">No rows{{if .Offset}} on this page{{end}}.</td></tr>{{end}}</tbody>
  </table></div>
  <p class="pager">{{if .Offset}}<a href="/d1/{{q .DB}}/{{q .Table}}?offset={{.Prev}}">← Previous</a>{{end}}{{if .More}}<a href="/d1/{{q .DB}}/{{q .Table}}?offset={{.Next}}">Next →</a>{{end}}</p>
</section>`)

// devCell is one rendered D1 value.
type devCell struct {
	Text string
	Null bool
}

func (c *devConsole) d1Rows(w http.ResponseWriter, r *http.Request) {
	db, ok := c.database(r.PathValue("db"))
	if !ok {
		c.render(w, 404, "D1", nil, []string{"The app binds no D1 database " + strconv.Quote(r.PathValue("db")) + "."}, nil)
		return
	}
	table := r.PathValue("table")
	tables, err := c.listD1Tables(r.Context(), db.Database)
	if err != nil {
		c.render(w, 502, "D1", nil, []string{"D1: " + err.Error()}, nil)
		return
	}
	known := false
	for _, t := range tables {
		known = known || t.Name == table
	}
	if !known {
		c.render(w, 404, "D1", nil, []string{"Database " + strconv.Quote(db.Database) + " has no table " + strconv.Quote(table) + "."}, nil)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	sql := fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", sqlIdent(table), devConsoleRowsPerPage+1, offset)
	rs, err := c.d1(r.Context(), db.Database, []d1mig.Query{{SQL: sql}})
	if err == nil && len(rs) != 1 {
		err = fmt.Errorf("D1 answered %d results for 1 query", len(rs))
	}
	if err == nil && !rs[0].Success && rs[0].Error != "" {
		err = fmt.Errorf("%s", rs[0].Error)
	}
	if err != nil {
		c.render(w, 502, "D1", nil, []string{"D1: " + err.Error()}, nil)
		return
	}
	raw := rs[0].Results.Rows
	more := len(raw) > devConsoleRowsPerPage
	if more {
		raw = raw[:devConsoleRowsPerPage]
	}
	rows := make([][]devCell, len(raw))
	for i, row := range raw {
		for _, v := range row {
			rows[i] = append(rows[i], d1Cell(v))
		}
	}
	c.render(w, 200, "D1 "+table, devD1RowsBody, nil, map[string]any{
		"DB": db.Database, "Table": table, "Columns": rs[0].Results.Columns, "Rows": rows,
		"Offset": offset, "Prev": max(0, offset-devConsoleRowsPerPage), "Next": offset + devConsoleRowsPerPage, "More": more,
		"First": offset + 1, "Last": offset + len(rows),
	})
}

// d1Cell renders one D1 value: text as is, numbers without exponent,
// anything else (a BLOB's byte array) as JSON.
func d1Cell(v any) devCell {
	switch x := v.(type) {
	case nil:
		return devCell{Null: true}
	case string:
		return devCell{Text: truncateRunes(x, 2*devConsolePreview)}
	case float64:
		return devCell{Text: strconv.FormatFloat(x, 'f', -1, 64)}
	default:
		b, _ := json.Marshal(x)
		return devCell{Text: truncateRunes(string(b), 2*devConsolePreview)}
	}
}
