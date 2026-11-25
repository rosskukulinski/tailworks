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

// `tw telemetry set|show|rm` from a linked machine (ADR-045):
// a real tw-mgmt in process behind its WhoIs gate. A header given by
// name is read from stdin and never printed again.
func TestCLITelemetry(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twtel-")
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

	const token = "Bearer otlp-cli-s3cret"
	oldIn := secretInput
	secretInput = func() (io.Reader, bool, func() ([]byte, error)) { return strings.NewReader(token + "\n"), false, nil }
	t.Cleanup(func() { secretInput = oldIn })
	run := func(args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		err := cmdTelemetry(append([]string{"--state-dir", devDir}, args...))
		w.Close()
		os.Stdout = old
		return <-done, err
	}

	out, err := run()
	if err != nil || !strings.Contains(out, "exports nothing over OTLP") {
		t.Fatalf("show (none): %q %v", out, err)
	}
	out, err = run("set", "--otlp", "http://otel-collector:4318", "--header", "authorization", "--header", "x-scope-orgid=acme", "--signals", "logs,traces")
	if err != nil || !strings.Contains(out, "endpoint  http://otel-collector:4318") || !strings.Contains(out, "headers   authorization, x-scope-orgid") ||
		!strings.Contains(out, "signals   logs, traces") || strings.Contains(out, "s3cret") {
		t.Fatalf("set: %q %v", out, err)
	}
	out, err = run("show", "--json")
	if err != nil || !strings.Contains(out, `"headerNames": [`) || strings.Contains(out, "s3cret") || strings.Contains(out, "acme") {
		t.Fatalf("show --json: %q %v", out, err)
	}
	if g, err := m.ReleaseTelemetry(ctx, "tn_a", "w", "", 1); err == nil || g != nil {
		t.Fatalf("a release without a lease: %v %v", g, err)
	}
	for _, args := range [][]string{
		{"set"},
		{"set", "--otlp", "otel:4318"},
		{"show", "--otlp", "http://x"},
		{"frobnicate"},
	} {
		if _, err := run(args...); cliCode(err) != "bad_request" {
			t.Errorf("telemetry %v: %v", args, err)
		}
	}
	if out, err = run("rm"); err != nil || !strings.Contains(out, "OTLP export off") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if out, _ = run(); !strings.Contains(out, "exports nothing") {
		t.Fatalf("after rm: %q", out)
	}
}
