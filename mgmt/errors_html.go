package mgmt

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

// Error pages for browsers on the admin UI's entry points (issue #497).
//
// TailnetHandler and the Host guard run before mountUI, so a teammate who
// opens the printed /ui/ URL without the admin capability, from off the
// tailnet, or through a CNAME used to get the JSON error body: 403
// not_admin with the grant snippet escaped into one line, 401 no_identity,
// 421 misdirected_request. When the request is a GET or HEAD of /, /ui or
// /ui/... and its Accept header prefers text/html to JSON (wantsHTML),
// writeErr renders the same error as a standalone HTML page instead.
//
// Only the serialization changes: the status, the Tw-Error and Retry-After
// headers and what was authorized are exactly what the JSON response has.
// The page shows no more than the JSON body does (less for
// misdirected_request: the message, which echoes the request's Host, is
// left out and the fix names mgmt's own names from the allowlist).
// Everything goes through html/template, so nothing is unescaped. /v1,
// /mcp and every client without an explicit text/html preference (the
// CLI, hostd, curl's */*) keep the JSON body unchanged.

// wantsHTML reports whether r's Accept header prefers text/html to JSON.
// It has netd.WantsHTML's semantics (mgmt does not import netd): only an
// explicit text/html (or text/*) counts; a wildcard alone (*/*) keeps
// JSON, and a tie keeps JSON.
func wantsHTML(r *http.Request) bool {
	htmlQ, jsonQ, anyQ := -1.0, -1.0, -1.0
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		switch mt {
		case "text/html", "application/xhtml+xml":
			htmlQ = max(htmlQ, q)
		case "text/*":
			htmlQ = max(htmlQ, q-0.0001) // less specific than text/html
		case "application/json", "application/*":
			jsonQ = max(jsonQ, q)
		case "*/*":
			anyQ = max(anyQ, q)
		}
	}
	if jsonQ < 0 {
		jsonQ = anyQ
	}
	return htmlQ > 0 && htmlQ > jsonQ
}

// htmlErrPage reports whether writeErr should answer r with the HTML page:
// a GET or HEAD of the admin UI's entry points from a browser.
func htmlErrPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p != "/" && p != "/ui" && !strings.HasPrefix(p, "/ui/") {
		return false
	}
	return wantsHTML(r)
}

// htmlErrFix is the fix sentence the page shows per code.
var htmlErrFix = map[string]string{
	"no_identity": "Open this from a device on the tailnet.",
	"not_admin":   "Ask your tailnet admin to add this grant to the tailnet policy, toward tag:tw-mgmt. Members of autogroup:admin already have it. Or use tw on the host.",
}

var errHTMLPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Status}} {{.StatusText}} · tailworks</title>
<style>
body { font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; margin: 0; padding: 12vh 24px; color: #1d2330; background: #f6f7f9; }
main { max-width: 40rem; margin: 0 auto; }
h1 { font-size: 1.6rem; margin: 0 0 .5rem; }
code, pre { font: .9em ui-monospace, Menlo, monospace; background: #e9ecf1; border-radius: 4px; }
code { padding: .1em .35em; }
pre { padding: .75em 1em; overflow-x: auto; white-space: pre; }
p.small { color: #677084; font-size: .9rem; }
a { color: #2459c9; }
@media (prefers-color-scheme: dark) { body { color: #e5e8ee; background: #101318; } code, pre { background: #232832; } p.small { color: #9ba3b2; } a { color: #82b1ff; } }
</style>
</head>
<body>
<main>
<h1>{{.Status}} {{.StatusText}}</h1>
{{if .Message}}<p>{{.Message}}</p>
{{end}}{{if .Fix}}<p>{{.Fix}}</p>
{{end}}{{if .Names}}<ul>
{{range .Names}}<li><code>{{.}}</code></li>
{{end}}</ul>
{{end}}{{if .Snippet}}<pre>{{.Snippet}}</pre>
{{end}}<p class="small">Error code <code>{{.Code}}</code>{{if .RetryAfter}} · retry after {{.RetryAfter}} s{{end}}{{if .TraceID}} · trace <code>{{.TraceID}}</code>{{end}} · <a href="{{.DocURL}}" rel="noreferrer">what this means</a></p>
</main>
</body>
</html>
`))

// writeErrHTML renders e (already completed by writeErr: status, doc URL,
// trace ID, Tw-Error and Retry-After headers set) as the HTML page.
func writeErrHTML(w http.ResponseWriter, e *Error) {
	page := struct {
		Status                                  int
		StatusText, Code, Message, Fix, Snippet string
		DocURL, TraceID, RetryAfter             string
		Names                                   []string
	}{
		Status:     e.Status,
		StatusText: http.StatusText(e.Status),
		Code:       e.Code,
		Message:    e.Message,
		Fix:        htmlErrFix[e.Code],
		Snippet:    e.Snippet,
		DocURL:     DocBase + e.Code,
		TraceID:    e.TraceID,
		RetryAfter: w.Header().Get("Retry-After"),
	}
	if e.Code == "misdirected_request" {
		// The message echoes the request's Host; name mgmt's own names
		// (from the allowlist only) instead.
		page.Message = ""
		page.Names = e.hostNames
		page.Fix = "Open mgmt by its tailnet IP or one of its own names:"
		if len(page.Names) == 0 {
			page.Fix = "Open mgmt by its tailnet IP."
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Vary", "Accept")
	w.WriteHeader(e.Status)
	_ = errHTMLPage.Execute(w, page)
}
