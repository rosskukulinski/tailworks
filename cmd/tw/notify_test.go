package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// `tw notify set|show|test|rm` from a linked machine
// (ADR-047): a real tw-mgmt behind its WhoIs gate.
// The webhook URL read from stdin is never printed again, only its origin.
func TestCLINotify(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twntf-")
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
	srv := &mgmt.Server{M: m}
	api := httptest.NewServer(srv.TailnetHandler(func(context.Context, string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nDEV", Login: "dev@example.com", Caps: []string{mgmt.AdminCap}}, nil
	}, srv.Handler()))
	t.Cleanup(api.Close)
	devDir := filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o700)
	os.WriteFile(filepath.Join(devDir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-14T00:00:00Z"}`), 0o600)

	const hook = "http://127.0.0.1:9/services/T0/B0/hook-s3cret"
	oldIn := secretInput
	secretInput = func() (io.Reader, bool, func() ([]byte, error)) { return strings.NewReader(hook + "\n"), false, nil }
	t.Cleanup(func() { secretInput = oldIn })
	run := func(args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		err := cmdNotify(append([]string{"--state-dir", devDir}, args...))
		w.Close()
		os.Stdout = old
		return <-done, err
	}

	if out, err := run(); err != nil || !strings.Contains(out, "sends no notifications") {
		t.Fatalf("show (none): %q %v", out, err)
	}
	out, err := run("set", "--webhook", "-", "--header", "x-team=support")
	if err != nil || !strings.Contains(out, "webhook   http://127.0.0.1:9/…") || !strings.Contains(out, "events    approval_requested") ||
		!strings.Contains(out, "headers   x-team") || strings.Contains(out, "s3cret") {
		t.Fatalf("set: %q %v", out, err)
	}
	if out, err := run("show", "--json"); err != nil || !strings.Contains(out, `"origin": "http://127.0.0.1:9"`) || strings.Contains(out, "s3cret") {
		t.Fatalf("show --json: %q %v", out, err)
	}
	// mgmt refuses to deliver to loopback; the outcome says so, not the URL.
	if out, err := run("test"); err != nil || !strings.Contains(out, "not delivered") || !strings.Contains(out, "not allowed") || strings.Contains(out, "s3cret") {
		t.Fatalf("test: %q %v", out, err)
	}
	for _, args := range [][]string{
		{"set"},
		{"set", "--webhook", "hooks.example.com/x"},
		{"show", "--webhook", "http://x"},
		{"frobnicate"},
	} {
		if _, err := run(args...); cliCode(err) != "bad_request" {
			t.Errorf("notify %v: %v", args, err)
		}
	}
	if out, err := run("rm"); err != nil || !strings.Contains(out, "notifications off") {
		t.Fatalf("rm: %q %v", out, err)
	}
}
