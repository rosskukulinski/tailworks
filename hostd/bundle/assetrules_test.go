package bundle

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseRedirects(t *testing.T) {
	rs, err := ParseRedirects([]byte(`# comment

/old            /new
/moved          /new        301
/blog/*         /posts/:splat
/u/:id/profile  /people/:id 308
/docs/*         https://docs.example.com/:splat 302
/app/*          /index.html 200
/port           https://example.com:8443/x
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 7 {
		t.Fatalf("%d rules: %+v", len(rs), rs)
	}
	for i, want := range []struct {
		to     string
		status int
		re     string
	}{
		{"/new", 302, `^/old$`},
		{"/new", 301, `^/moved$`},
		{"/posts/:splat", 302, `^/blog/(?<splat>.*)$`},
		{"/people/:id", 308, `^/u/(?<id>[^/]+)/profile$`},
		{"https://docs.example.com/:splat", 302, `^/docs/(?<splat>.*)$`},
		{"/index.html", 200, `^/app/(?<splat>.*)$`},
		{"https://example.com:8443/x", 302, `^/port$`},
	} {
		if r := rs[i]; r.To != want.to || r.Status != want.status || r.RE != want.re {
			t.Errorf("rule %d: %+v, want %+v", i, r, want)
		}
	}
	// Regexp metacharacters in a path are literal.
	if rs, _ := ParseRedirects([]byte("/a.b+(c) /x")); rs[0].RE != `^/a\.b\+\(c\)$` {
		t.Errorf("escaping: %s", rs[0].RE)
	}
}

func TestParseRedirectsInvalid(t *testing.T) {
	for in, want := range map[string]string{
		"/a":                        "want `<from> <to> [status]`",
		"/a /b 404":                 "status 404",
		"/a /b x":                   "not a number",
		"a /b":                      "must start with /",
		"https://x.example/a /b":    "no domain-level rules",
		"/a/* /b/*/* 301":           "",
		"/a/*/* /b":                 "at most one *",
		"/a /b/:splat":              ":splat is not captured",
		"/u/:id /p/:name":           ":name is not captured",
		"/a https://x.example/ 200": "200 rewrite must name a path",
		"/a b":                      "want a path",
		"/:splat /b":                "reserved",
		"/:id/:id /b":               "repeated",
		"/" + strings.Repeat("a", MaxRedirectLine) + " /b": "longer than",
	} {
		_, err := ParseRedirects([]byte("# header\n" + in))
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "_redirects:2:") {
			t.Errorf("%q: %v, want _redirects:2: …%s", in, err, want)
		}
	}
	var b strings.Builder
	for i := 0; i <= MaxDynamicRedirects; i++ {
		fmt.Fprintf(&b, "/d%d/* /x\n", i)
	}
	if _, err := ParseRedirects([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Errorf("too many dynamic rules: %v", err)
	}
}

func TestParseHeaders(t *testing.T) {
	hs, err := ParseHeaders([]byte(`# comment
/*
  X-Frame-Options: DENY
  Link: </style.css>; rel=preload; as=style

/assets/*
	Cache-Control: public, max-age=31536000, immutable
  ! X-Frame-Options

/u/:id
  X-User: :id
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 3 {
		t.Fatalf("%d rules: %+v", len(hs), hs)
	}
	if h := hs[0]; h.RE != `^/(?<splat>.*)$` || len(h.Set) != 2 || h.Set[0] != [2]string{"x-frame-options", "DENY"} || h.Set[1][1] != "</style.css>; rel=preload; as=style" {
		t.Errorf("rule 0: %+v", h)
	}
	if h := hs[1]; h.Set[0] != [2]string{"cache-control", "public, max-age=31536000, immutable"} || len(h.Unset) != 1 || h.Unset[0] != "x-frame-options" {
		t.Errorf("rule 1: %+v", h)
	}
	if h := hs[2]; h.RE != `^/u/(?<id>[^/]+)$` || h.Set[0][1] != ":id" {
		t.Errorf("rule 2: %+v", h)
	}
}

func TestParseHeadersInvalid(t *testing.T) {
	for in, want := range map[string]string{
		"  X-A: 1":          "needs a path line",
		"/a\n  no colon":    "want `Name: value`",
		"/a\n  Bad Name: 1": "want `Name: value`",
		"/a\n  ! bad name":  "bad header name",
		"a\n  X-A: 1":       "must start with /",
		"/*/*\n  X-A: 1":    "at most one *",
	} {
		if _, err := ParseHeaders([]byte(in)); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "_headers:") {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
	var b strings.Builder
	for i := 0; i <= MaxHeaderRules; i++ {
		fmt.Fprintf(&b, "/p%d\n  X-A: 1\n", i)
	}
	if _, err := ParseHeaders([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "rules") {
		t.Errorf("too many rules: %v", err)
	}
}

// TestParseAssetsRulesAndOnly: _redirects/_headers are parsed out of the
// files (not served); no main makes an assets-only app with hostd's Worker.
func TestParseAssetsRulesAndOnly(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc":      []byte(`{"name":"w","assets":{"directory":"dist"}}`),
		"assets/index.html":   []byte("hi"),
		"assets/_redirects":   []byte("/a /b"),
		"assets/_headers":     []byte("/*\n  X-A: 1"),
		"assets/sub/_headers": []byte("served: only the root one is read"),
	}
	a, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	as := a.Assets
	if !as.Only || a.Main != AssetsOnlyMain || a.Modules[AssetsOnlyMain] == nil {
		t.Fatalf("main %q only %v", a.Main, as.Only)
	}
	if got := strings.Join(as.AssetPaths(), ","); got != "/index.html,/sub/_headers" {
		t.Errorf("files: %s", got)
	}
	if len(as.Redirects) != 1 || len(as.Headers) != 1 {
		t.Errorf("rules: %+v %+v", as.Redirects, as.Headers)
	}
	files["assets/_redirects"] = []byte("/a")
	if _, err := Parse(files); err == nil || !strings.Contains(err.Error(), "_redirects:1:") {
		t.Errorf("bad _redirects: %v", err)
	}
	files["assets/_redirects"] = []byte("/a /b")
	files["wrangler.jsonc"] = []byte(`{"name":"w","assets":{"directory":"dist","run_worker_first":["/api/*"]}}`)
	if _, err := Parse(files); err == nil || !strings.Contains(err.Error(), "needs a Worker") {
		t.Errorf("assets-only run_worker_first: %v", err)
	}
	// No main and no assets is still an error.
	if _, err := Parse(map[string][]byte{"wrangler.jsonc": []byte(`{"name":"w"}`)}); err == nil || !strings.Contains(err.Error(), "no main") {
		t.Errorf("no main: %v", err)
	}
}
