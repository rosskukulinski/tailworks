package main

import (
	"bytes"
	"strings"
	"testing"
)

// tailnet.https is in the tailworks.jsonc schema: a boolean, nothing else.
func TestTwSchemaTailnetHTTPS(t *testing.T) {
	if err := checkTwSchema([]byte(`{"name": "a", "tailnet": {"tags": ["tag:a"], "https": true}}`)); err != nil {
		t.Errorf("tailnet.https: true refused: %v", err)
	}
	if err := checkTwSchema([]byte(`{"name": "a", "tailnet": {"tags": ["tag:a"], "https": "yes"}}`)); err == nil {
		t.Error(`tailnet.https: "yes" accepted`)
	}
}

func TestWorkloadURLTailnetHTTPS(t *testing.T) {
	for raw, want := range map[string]string{
		``:                                 "http://app.t.ts.net/",
		`{"tailnet": {"tags": ["tag:a"]}}`: "http://app.t.ts.net/",
		`{"tailnet": {"tags": ["tag:a"], "https": false}}`:                   "http://app.t.ts.net/",
		"// on\n{\"tailnet\": {\"tags\": [\"tag:a\"], \"https\": true,},}\n": "https://app.t.ts.net/",
		`not json`: "http://app.t.ts.net/",
	} {
		if got := workloadURL([]byte(raw), "app.t.ts.net"); got != want {
			t.Errorf("%q: %s, want %s", raw, got, want)
		}
	}
	var b bytes.Buffer
	printDevTailnetHTTPS([]byte(`{"tailnet": {"https": true}}`), &b)
	if !strings.Contains(b.String(), "http:// URL only") {
		t.Errorf("tw dev note: %q", b.String())
	}
	b.Reset()
	printDevTailnetHTTPS([]byte(`{"tailnet": {}}`), &b)
	if b.Len() != 0 {
		t.Errorf("tw dev note without tailnet.https: %q", b.String())
	}
}
