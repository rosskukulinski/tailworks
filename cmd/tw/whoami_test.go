package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/schema"
)

// TestWhoami: `tw whoami` on a linked machine asks a real tw-mgmt (behind
// its WhoIs gate) who it is, and reports its capabilities: an admin, and an
// approver who isn't an admin but holds an MCP grant. The --json output
// matches schema/cli.schema.json $defs/whoami.
func TestWhoami(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twwho-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx := context.Background()
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "api.env")
	writeFakeOAuthFile(t, keyFile)
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, Broker: &mgmt.Broker{BaseURL: "http://tsapi.invalid"},
		IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Hour, PolicyMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.CreateTenant(ctx, mgmt.TenantRequest{ID: "tn_a", Tailnet: "a.ts.net", MintMode: mgmt.MintOAuth, OAuthClientFile: keyFile}); err != nil {
		t.Fatal(err)
	}

	who := map[string]mgmt.WhoIsInfo{
		"alice": {NodeID: "nALICE", Name: "alice-laptop.a.ts.net.", Login: "alice@example.com", Caps: []string{mgmt.AdminCap}},
		"bob": {NodeID: "nBOB", Name: "bob-laptop.a.ts.net.", Login: "bob@example.com", Caps: []string{approvecap.Cap, mgmt.MCPCap},
			CapValues: map[string][]json.RawMessage{
				approvecap.Cap: {json.RawMessage(`{"workflows":["refund"]}`)},
				mgmt.MCPCap:    {json.RawMessage(`{"tools":["deploy"]}`)},
			}},
	}
	for _, name := range []string{"alice", "bob"} {
		t.Run(name, func(t *testing.T) {
			srv := &mgmt.Server{M: m}
			api := httptest.NewServer(srv.TailnetHandler(func(context.Context, string) (mgmt.WhoIsInfo, error) {
				return who[name], nil
			}, srv.Handler()))
			t.Cleanup(api.Close)
			devDir := filepath.Join(dir, name)
			os.MkdirAll(devDir, 0o700)
			os.WriteFile(filepath.Join(devDir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-15T00:00:00Z"}`), 0o600)

			out, err := captureStdout(t, func() error { return cmdWhoami([]string{"--state-dir", devDir, "--json"}) })
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatalf("not JSON: %q", out)
			}
			probs, err := schema.CheckCLI("whoami", v)
			if err != nil || len(probs) > 0 {
				t.Fatalf("whoami --json vs schema: %v %v\n%s", probs, err, out)
			}
			var r whoamiResult
			json.Unmarshal([]byte(out), &r)
			if r.Kind != "user" || !r.Linked || r.Mgmt.Endpoint != api.URL || r.Can.Approve == nil || r.Can.MCPTools == nil {
				t.Fatalf("whoami: %+v", r)
			}
			switch name {
			case "alice":
				if r.Actor != "alice@example.com" || !r.Can.Admin || *r.Can.Approve || !slices.Equal(r.Caps, []string{mgmt.AdminCap}) ||
					r.Tenant == nil || r.Tenant.TenantID != "tn_a" || !slices.Contains(r.Can.MCPTools, "ls") || slices.Contains(r.Can.MCPTools, "deploy") {
					t.Fatalf("admin: %+v", r)
				}
			case "bob":
				if r.Can.Admin || !*r.Can.Approve || !slices.Equal(r.Caps, []string{approvecap.Cap, mgmt.MCPCap}) || r.Tenant != nil ||
					!slices.Contains(r.Can.MCPTools, "deploy") || !slices.Contains(r.Can.MCPTools, "whoami") {
					t.Fatalf("approver: %+v", r)
				}
			}

			// The console URL is one mgmt's UI gate (mountUI) lets this
			// caller open: /ui/ for the admin, the approvals page for bob.
			wantConsole := map[string]string{"alice": api.URL + "/ui/", "bob": api.URL + "/ui/approvals"}[name]
			if r.Console != wantConsole {
				t.Fatalf("console = %q, want %q", r.Console, wantConsole)
			}
			if resp, err := api.Client().Get(r.Console); err != nil || resp.StatusCode != 200 {
				t.Fatalf("GET %s: %v %v", r.Console, resp, err)
			} else {
				resp.Body.Close()
			}

			human, err := captureStdout(t, func() error { return cmdWhoami([]string{"--state-dir", devDir}) })
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{who[name].Login, "via tw link", api.URL, "can       admin"} {
				if !strings.Contains(human, want) {
					t.Errorf("whoami lacks %q:\n%s", want, human)
				}
			}
			if name == "bob" && (!strings.Contains(human, "approve yes") || !strings.Contains(human, "not a tailworks admin")) {
				t.Errorf("approver output:\n%s", human)
			}
		})
	}
}

// TestWhoamiOldMgmt: a mgmt from before /v1/whoami said approver and
// mcpTools leaves them null (unknown), not false.
func TestWhoamiOldMgmt(t *testing.T) {
	t.Parallel()
	var w whoamiWire
	json.Unmarshal([]byte(`{"kind":"user","login":"a@b","admin":true,"actor":"a@b","mgmt":{"https":true}}`), &w)
	r := whoamiFrom(&state{Dir: t.TempDir(), Link: &Link{Endpoint: "https://tw-mgmt.x.ts.net"}}, w)
	if r.Can.Approve != nil || r.Can.MCPTools != nil {
		t.Fatalf("old mgmt: %+v", r.Can)
	}
	b, _ := json.Marshal(r)
	var v map[string]any
	json.Unmarshal(b, &v)
	if probs, err := schema.CheckCLI("whoami", v); err != nil || len(probs) > 0 {
		t.Fatalf("schema: %v %v", probs, err)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err := fn()
	w.Close()
	os.Stdout = old
	return <-done, err
}
