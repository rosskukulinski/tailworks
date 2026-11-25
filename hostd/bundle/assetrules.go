package bundle

// _redirects and _headers (Cloudflare's static-assets control files,
// ADR-050): parsed here, once, for tw (which reports a bad line
// before it uploads) and hostd (which hands the compiled rules to the
// router Worker, hostd/gen/js/assets.js). Patterns compile to JavaScript
// regular expressions: `*` is a splat (any characters, `/` too, captured as
// :splat) and a `:name` path segment is a placeholder (one segment). Rules
// match the request's URL path as sent (percent-encoded).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Cloudflare's limits for the two files.
const (
	MaxStaticRedirects  = 2000
	MaxDynamicRedirects = 100
	MaxRedirectLine     = 1000
	MaxHeaderRules      = 100
	MaxHeaderLine       = 2000
)

// Redirect is one _redirects line.
type Redirect struct {
	From   string `json:"from"`
	RE     string `json:"re"` // JS regexp source for From
	To     string `json:"to"`
	Status int    `json:"status"`
}

// HeaderRule is one _headers block: a path pattern and the headers it sets
// (in order) or removes ("! Name").
type HeaderRule struct {
	Path  string      `json:"path"`
	RE    string      `json:"re"`
	Set   [][2]string `json:"set"`
	Unset []string    `json:"unset"`
}

var (
	placeholderRE = regexp.MustCompile(`^:([A-Za-z][A-Za-z0-9_]*)$`)
	refRE         = regexp.MustCompile(`:([A-Za-z][A-Za-z0-9_]*)`)
	headerNameRE  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// compilePattern turns a path pattern into a JS regexp source and the
// names it captures ("splat" for `*`).
func compilePattern(p string) (string, []string, error) {
	if !strings.HasPrefix(p, "/") {
		if strings.Contains(p, "://") {
			return "", nil, fmt.Errorf("%q: only paths are supported (no domain-level rules)", p)
		}
		return "", nil, fmt.Errorf("%q: a path must start with /", p)
	}
	if strings.Count(p, "*") > 1 {
		return "", nil, fmt.Errorf("%q: at most one * (splat)", p)
	}
	var b strings.Builder
	var names []string
	seen := map[string]bool{}
	b.WriteString("^")
	for i, seg := range strings.Split(p, "/") {
		if i > 0 {
			b.WriteString("/")
		}
		if m := placeholderRE.FindStringSubmatch(seg); m != nil {
			if m[1] == "splat" || seen[m[1]] {
				return "", nil, fmt.Errorf("%q: placeholder :%s is reserved or repeated", p, m[1])
			}
			seen[m[1]] = true
			names = append(names, m[1])
			b.WriteString("(?<" + m[1] + ">[^/]+)")
			continue
		}
		parts := strings.Split(seg, "*")
		for j, part := range parts {
			if j > 0 {
				names = append(names, "splat")
				b.WriteString("(?<splat>.*)")
			}
			b.WriteString(regexp.QuoteMeta(part))
		}
	}
	b.WriteString("$")
	return b.String(), names, nil
}

// checkRefs fails when s uses a :placeholder its pattern doesn't capture.
func checkRefs(s string, names []string) error {
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		ok := false
		for _, n := range names {
			ok = ok || n == m[1]
		}
		// "https://host:8443/" has a port, not a placeholder.
		if !ok && !regexp.MustCompile(`^\d`).MatchString(m[1]) {
			return fmt.Errorf(":%s is not captured by the path pattern", m[1])
		}
	}
	return nil
}

// ParseRedirects parses a _redirects file.
func ParseRedirects(b []byte) ([]Redirect, error) {
	var out []Redirect
	static, dynamic := 0, 0
	for i, line := range strings.Split(string(b), "\n") {
		n := i + 1
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > MaxRedirectLine {
			return nil, fmt.Errorf("_redirects:%d: longer than %d characters", n, MaxRedirectLine)
		}
		f := strings.Fields(line)
		if len(f) != 2 && len(f) != 3 {
			return nil, fmt.Errorf("_redirects:%d: want `<from> <to> [status]`", n)
		}
		r := Redirect{From: f[0], To: f[1], Status: 302}
		if len(f) == 3 {
			s, err := strconv.Atoi(f[2])
			if err != nil {
				return nil, fmt.Errorf("_redirects:%d: status %q is not a number", n, f[2])
			}
			r.Status = s
		}
		switch r.Status {
		case 200, 301, 302, 303, 307, 308:
		default:
			return nil, fmt.Errorf("_redirects:%d: status %d: want 200 (rewrite), 301, 302, 303, 307 or 308", n, r.Status)
		}
		re, names, err := compilePattern(r.From)
		if err != nil {
			return nil, fmt.Errorf("_redirects:%d: %v", n, err)
		}
		r.RE = re
		external := strings.HasPrefix(r.To, "http://") || strings.HasPrefix(r.To, "https://")
		if !external && !strings.HasPrefix(r.To, "/") {
			return nil, fmt.Errorf("_redirects:%d: destination %q: want a path (/…) or an http(s) URL", n, r.To)
		}
		if r.Status == 200 && external {
			return nil, fmt.Errorf("_redirects:%d: a 200 rewrite must name a path on this site, not %q", n, r.To)
		}
		if err := checkRefs(r.To, names); err != nil {
			return nil, fmt.Errorf("_redirects:%d: %v", n, err)
		}
		if len(names) > 0 {
			dynamic++
		} else {
			static++
		}
		out = append(out, r)
	}
	if static > MaxStaticRedirects || dynamic > MaxDynamicRedirects {
		return nil, fmt.Errorf("_redirects: %d static and %d dynamic rules; at most %d and %d", static, dynamic, MaxStaticRedirects, MaxDynamicRedirects)
	}
	return out, nil
}

// ParseHeaders parses a _headers file.
func ParseHeaders(b []byte) ([]HeaderRule, error) {
	var out []HeaderRule
	cur := -1 // index of the rule header lines add to
	for i, raw := range strings.Split(string(b), "\n") {
		n := i + 1
		raw = strings.TrimSuffix(raw, "\r")
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(raw) > MaxHeaderLine {
			return nil, fmt.Errorf("_headers:%d: longer than %d characters", n, MaxHeaderLine)
		}
		if raw[0] != ' ' && raw[0] != '\t' {
			re, _, err := compilePattern(line)
			if err != nil {
				return nil, fmt.Errorf("_headers:%d: %v", n, err)
			}
			out = append(out, HeaderRule{Path: line, RE: re, Set: [][2]string{}, Unset: []string{}})
			cur = len(out) - 1
			continue
		}
		if cur < 0 {
			return nil, fmt.Errorf("_headers:%d: a header line needs a path line above it", n)
		}
		if strings.HasPrefix(line, "!") {
			h := strings.TrimSpace(line[1:])
			if !headerNameRE.MatchString(h) {
				return nil, fmt.Errorf("_headers:%d: bad header name %q", n, h)
			}
			out[cur].Unset = append(out[cur].Unset, strings.ToLower(h))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !headerNameRE.MatchString(k) {
			return nil, fmt.Errorf("_headers:%d: want `Name: value` or `! Name`", n)
		}
		out[cur].Set = append(out[cur].Set, [2]string{strings.ToLower(k), v})
	}
	if len(out) > MaxHeaderRules {
		return nil, fmt.Errorf("_headers: %d rules; at most %d", len(out), MaxHeaderRules)
	}
	return out, nil
}
