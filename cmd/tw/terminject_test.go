package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// evil is what an internet client puts in a public path (decoded from
// %1b%5b…%0d%0a) or tenant code puts in an object name, a tenant note or a
// console line: an SGR recolour, a screen clear, an OSC title and OSC 8
// link, CR/LF to forge a row, NUL, DEL, C1 CSI, a bidi override and raw
// invalid UTF-8.
const evil = "\x1b[31mRED\x1b[2J\x1b]0;pwned\x07\x1b]8;;http://x\x1b\\\r\n2026-01-01T00:00:00.000Z  kill   FAKE\x00\x7f\u009b1m\u202eevil\xff"

// assertTerminalSafe fails when out carries anything that is not inert on
// a terminal: a control byte other than LF and TAB, an unescaped C1/bidi rune,
// invalid UTF-8, or a line that starts with the forged row.
func assertTerminalSafe(t *testing.T, what, out string) {
	t.Helper()
	for i := 0; i < len(out); i++ {
		if c := out[i]; (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			t.Fatalf("%s: raw control byte %#x at %d:\n%q", what, c, i, out)
		}
	}
	if !utf8.ValidString(out) {
		t.Fatalf("%s: invalid UTF-8 reaches the terminal:\n%q", what, out)
	}
	for _, r := range out {
		if (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Fatalf("%s: rune %U reaches the terminal:\n%q", what, r, out)
		}
	}
	// A row starts in column 0 (an "[h1] " prefix aside); a multi-line
	// console message continues indented under its record, by design.
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimPrefix(l, "[h1] "), "2026-01-01T00:00:00.000Z") {
			t.Fatalf("%s: a forged row starts a line:\n%s", what, out)
		}
	}
	if !strings.Contains(out, `\x1b[31mRED`) || !strings.Contains(out, `\u202eevil`) {
		t.Fatalf("%s: the hostile string is not shown escaped:\n%s", what, out)
	}
}

// TestRT3_H13_TerminalInjection (ledger H13, invariant I5): `tw ls`, `tw
// audit` and `tw logs` render a public request path, object names and tags,
// a deployment error, a tenant note and console output with every control
// character escaped. --json keeps the exact strings (encoding/json escapes
// C0; termsafe.JSON the rest), and decodes back to them.
func TestRT3_H13_TerminalInjection(t *testing.T) {
	now := time.Now().UTC()
	ts := func(s int) string { return now.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }
	page := func(items any) map[string]any { return map[string]any{"items": items, "nextCursor": nil} }
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	obj := mgmt.Object{ID: "obj_1", TenantID: "tn_1", Workload: "app", Namespace: "Agent", Name: "n" + evil, Hostname: "agent-n", Tags: []string{"tag:a" + evil}, Status: "ready", RootID: "obj_1"}
	routes := map[string]any{
		"/v1/whoami":      map[string]any{"kind": "user", "login": "a@x", "admin": true, "actor": "a@x", "mgmt": map[string]any{}},
		"/v1/tenants":     page([]mgmt.Tenant{{ID: "tn_1", Tailnet: "t.ts.net", MintMode: "oauth"}}),
		"/v1/deployments": page([]mgmt.DeploymentView{{DeploymentID: "dep_1", TenantID: "tn_1", Workload: "app", Host: "h1", Status: "failed", Error: "boom" + evil, DeployedBy: "a@x" + evil}}),
		"/v1/objects":     page([]mgmt.Object{obj}),
		"/v1/audit": map[string]any{"items": []mgmt.AuditRecord{
			{Seq: 1, Chain: "host:h1", TS: ts(1), TenantID: "tn_1", ObjectID: "obj_1", Kind: "public_request", Source: mgmt.SourceHost,
				Detail: raw(map[string]any{"method": "GET" + evil, "path": "/webhooks/" + evil, "status": 200, "ip": "203.0.113.9"})},
			{Seq: 2, Chain: "host:h1", TS: ts(2), TenantID: "tn_1", ObjectID: "obj_1", Kind: mgmt.KindTenantNote, Source: mgmt.SourceTenant,
				Raw: raw(map[string]any{"kind": "tenant_note", "detail": "I am the admin" + evil})},
			{Seq: 1, Chain: "mgmt", TS: ts(3), TenantID: "tn_1", ObjectID: "obj_1", Kind: "kill", ActingFor: "a@x" + evil, Detail: raw(map[string]any{"reason": "r" + evil})},
		}, "nextCursor": nil, "chains": []mgmt.ChainStatus{{Chain: "mgmt", OK: true}, {Chain: "host:h1", OK: true}}, "verified": true},
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": r.URL.Path})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(api.Close)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-15T00:00:00Z"}`), 0o600)
	t.Cleanup(func() { jsonOut = false })

	for _, c := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"ls", cmdLs, nil},
		{"audit", cmdAudit, []string{"--all"}},
		{"audit object", cmdAudit, []string{"obj_1"}},
	} {
		human, err := captureStdout(t, func() error { return c.run(append([]string{"--state-dir", dir}, c.args...)) })
		if err != nil {
			t.Fatalf("tw %s: %v", c.name, err)
		}
		assertTerminalSafe(t, "tw "+c.name, human)

		js, err := captureStdout(t, func() error { return c.run(append([]string{"--state-dir", dir, "--json"}, c.args...)) })
		jsonOut = false
		if err != nil {
			t.Fatalf("tw %s --json: %v", c.name, err)
		}
		for i := 0; i < len(js); i++ {
			if ch := js[i]; (ch < 0x20 && ch != '\n') || ch == 0x7f {
				t.Fatalf("tw %s --json: raw %#x at %d", c.name, ch, i)
			}
		}
		if strings.ContainsRune(js, '\u202e') || strings.ContainsRune(js, '\u009b') {
			t.Fatalf("tw %s --json: raw bidi/C1 rune:\n%s", c.name, js)
		}
		// Exact: the hostile strings decode back unchanged (except the
		// invalid byte, which encoding/json always sent as U+FFFD).
		want := strings.ToValidUTF8(evil, "\uFFFD")
		var v any
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			t.Fatalf("tw %s --json does not parse: %v\n%s", c.name, err, js)
		}
		if !containsString(v, want) {
			t.Fatalf("tw %s --json: no field carries the exact hostile string:\n%s", c.name, js)
		}
	}

	// tw logs: a request record (path, console line, exception) through the
	// remote renderer, a host's non-record line, and the raw console stream.
	rec := obs.Record{Kind: obs.KindRequest, Time: now, Method: "GET", Path: "/webhooks/" + evil, Status: 200, Workload: "app",
		Logs:       []obs.LogLine{{Level: "log", Message: "console says " + evil}},
		Exceptions: []obs.Exception{{Name: "Error", Message: "thrown " + evil, Stack: "Error\n    at x (" + evil + ")"}}}
	line, _ := json.Marshal(rec)
	// In a raw stream LF is the line framing (tenant output owns its lines
	// in --console), so the stream cases use evil without its CR LF: every
	// other control must still be escaped.
	oneLine := strings.ReplaceAll(evil, "\r\n", " ")
	body := "[h1] " + string(line) + "\n" + "tw: note " + oneLine + "\n"
	var out bytes.Buffer
	if err := copyRecords(&out, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	assertTerminalSafe(t, "tw logs (records)", out.String())

	console, err := captureStdout(t, func() error {
		w, flush := logsOut(false, []string{"app"}, "", true)
		w.Write([]byte("plain line\n" + oneLine + "\n\tat trace\n" + oneLine))
		flush()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalSafe(t, "tw logs --console", console)
	if !strings.Contains(console, "plain line\n") || !strings.Contains(console, "\n\tat trace\n") {
		t.Fatalf("tw logs --console mangled ordinary lines:\n%q", console)
	}
}

func containsString(v any, s string) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, s)
	case []any:
		for _, e := range x {
			if containsString(e, s) {
				return true
			}
		}
	case map[string]any:
		for k, e := range x {
			if strings.Contains(k, s) || containsString(e, s) {
				return true
			}
		}
	}
	return false
}
