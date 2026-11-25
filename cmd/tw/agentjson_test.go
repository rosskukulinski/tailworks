package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A linked machine's replica lines arrive as "[<host>] …": with --json
// the prefix becomes "host"; anything else stays in "line".
func TestLogLinesSplitHost(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lw := &lineWriter{ev: newEvents(true, &buf), fields: map[string]any{"workload": "web"}, splitHost: true}
	lw.Write([]byte("[h1] hello\n[not a host] x\nplain"))
	lw.Flush()
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	if len(got) != 3 || got[0]["host"] != "h1" || got[0]["line"] != "hello" || got[1]["host"] != nil || got[1]["line"] != "[not a host] x" || got[2]["line"] != "plain" || got[2]["workload"] != "web" {
		t.Fatalf("%v", got)
	}
	if newEvents(false, &buf) != nil {
		t.Fatal("events without --json")
	}
	var nilEv *events
	nilEv.emit("x", nil) // a nil writer is a no-op
}

// Every --json error has code, message, hint and doc_url.
func TestJSONErrorHint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		e    *cliErr
		hint string
	}{
		{fail("acl_denied", "m").withFix("add this", "  snippet"), "add this\n  snippet"},
		{fail("bad_request", "m", "usage: tw x"), "usage: tw x"},
		{fail("killed", "m"), "see https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#killed"},
	} {
		got := jsonError(tc.e)
		if got.Hint != tc.hint || !strings.HasSuffix(got.DocURL, "#"+tc.e.Code) || tc.e.Hint != "" {
			t.Errorf("%s: hint %q doc_url %q", tc.e.Code, got.Hint, got.DocURL)
		}
	}
	if !wantsJSON([]string{"x", "--json"}) || wantsJSON([]string{"--", "--json"}) {
		t.Fatal("wantsJSON")
	}
}
