package netd

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

// Error pages for browsers on public (Funnel) routes (issue #50). A
// browser that hits rate_limited or not_public used to get a JSON body.
// When the request prefers text/html over JSON (WantsHTML), the public
// path answers with a minimal HTML page instead: the status, the code, a
// fixed sentence per code and the docs link. It carries no request or
// tenant data (no detail, no object ID), so nothing about the workload
// reaches an anonymous visitor beyond what the code says; JSON stays the
// default for every other client.

// WantsHTML reports whether r's Accept header prefers text/html to JSON.
// Only an explicit text/html (or text/*) counts; a wildcard alone (curl's
// */*) keeps JSON, and a tie keeps JSON.
func WantsHTML(r *http.Request) bool {
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

// htmlSummary is the one sentence an error page says per code.
var htmlSummary = map[string]string{
	CodeRateLimited:         "Too many requests. Wait a moment, then try again.",
	CodePayloadTooLarge:     "The request body is larger than this site accepts.",
	CodeHeaderTooLarge:      "The request headers are larger than this site accepts.",
	CodeNotPublic:           "There is no public page at this address.",
	CodeUpgradeNotAllowed:   "This address does not accept WebSocket or other upgraded connections.",
	CodeRequestTimeout:      "The site took too long to answer.",
	CodeKilled:              "This site is no longer running.",
	CodeObjectKilled:        "This site is no longer running.",
	CodeUpstreamUnavailable: "The site is not available right now. Try again shortly.",
	CodeBadRequest:          "The request was not understood.",
}

var errorPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Status}} {{.StatusText}}</title>
<style>
body { font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; margin: 0; padding: 12vh 24px; color: #1d2330; background: #f6f7f9; }
main { max-width: 34rem; margin: 0 auto; }
h1 { font-size: 1.6rem; margin: 0 0 .5rem; }
code { font: .9em ui-monospace, Menlo, monospace; background: #e9ecf1; padding: .1em .35em; border-radius: 4px; }
p.small { color: #677084; font-size: .9rem; }
a { color: #2459c9; }
@media (prefers-color-scheme: dark) { body { color: #e5e8ee; background: #101318; } code { background: #232832; } p.small { color: #9ba3b2; } a { color: #82b1ff; } }
</style>
</head>
<body>
<main>
<h1>{{.Status}} {{.StatusText}}</h1>
<p>{{.Summary}}</p>
<p class="small">Error code <code>{{.Code}}</code>{{if .RetryAfter}} · retry after {{.RetryAfter}} s{{end}} · <a href="{{.DocURL}}" rel="noreferrer">what this means</a></p>
</main>
</body>
</html>
`))

// WriteErrorHTML renders e as a minimal HTML page with e's status and
// code. The detail is never shown.
func WriteErrorHTML(w http.ResponseWriter, e *Error) {
	code := e.Code
	summary, ok := htmlSummary[code]
	if !ok {
		summary = "The request could not be completed."
	}
	h := w.Header()
	h.Set("content-type", "text/html; charset=utf-8")
	h.Set(HeaderError, code)
	h.Set("cache-control", "no-store")
	h.Set("x-content-type-options", "nosniff")
	h.Set("content-security-policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("vary", "Accept")
	status := e.Status()
	w.WriteHeader(status)
	_ = errorPage.Execute(w, struct {
		Status                            int
		StatusText, Summary, Code, DocURL string
		RetryAfter                        string
	}{status, http.StatusText(status), summary, code, DocBase + code, h.Get("Retry-After")})
}
