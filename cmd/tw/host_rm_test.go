package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// `tw host ls` and `tw host rm` on a linked machine, against a real
// tw-mgmt (issues #464, #477): host ls says what a host OFFERS (tiers and
// containers) and its ARCH; host rm refuses a host that holds leases and
// points at tw host drain first, and --force (or the deprecated --drain)
// removes it anyway.
func TestHostLsRmCLI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twhost-")
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
	for _, h := range []string{"h1", "h2"} {
		if _, _, err := m.RegisterHost(ctx, mgmt.RegisterRequest{HostID: h, CapacityNodes: 10, Tiers: []string{"process", "microvm", "containers"}, Arch: "arm64", Features: []string{"kv"}}, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateDeployment(ctx, mgmt.Deployment{ID: "dp_" + h, TenantID: "tn_a", Workload: "w-" + h, HostID: h, Epoch: 1, CreatedAt: 1,
			Namespaces: []mgmt.ClassConfig{{Class: mgmt.WorkloadNamespace, Tags: []string{"tag:w"}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcquireLease(ctx, mgmt.LeaseName("tn_a", "w-"+h), h, time.Minute, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	srv := &mgmt.Server{M: m}
	api := httptest.NewServer(srv.TailnetHandler(func(context.Context, string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nADM", Name: "admin-laptop.a.ts.net.", Login: "admin@example.com", Caps: []string{mgmt.AdminCap}}, nil
	}, srv.Handler()))
	t.Cleanup(api.Close)
	devDir := filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o700)
	os.WriteFile(filepath.Join(devDir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-14T00:00:00Z"}`), 0o600)
	run := func(args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		err := cmdHost(append(args, "--state-dir", devDir))
		w.Close()
		os.Stdout = old
		return <-done, err
	}

	out, err := run("ls")
	if err != nil || !strings.Contains(out, "OFFERS") || !strings.Contains(out, "ARCH") || strings.Contains(out, "TIERS") ||
		!strings.Contains(out, "process,microvm,containers arm64") {
		t.Fatalf("host ls: %q %v", out, err)
	}
	out, err = run("ls", "--json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 || rows[0]["arch"] != "arm64" {
		t.Fatalf("host ls --json: %q %v", out, err)
	}
	if fs, _ := rows[0]["features"].([]any); len(fs) != 1 || fs[0] != "kv" {
		t.Fatalf("host ls --json features: %q", out)
	}

	_, err = run("rm", "h1", "--yes")
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "host_holds_leases" || len(ce.Fix) != 2 ||
		!strings.Contains(ce.Fix[0], "tw host drain h1") || !strings.Contains(ce.Fix[1], "tw host rm h1 --force --yes") {
		t.Fatalf("host rm without --force: want host_holds_leases pointing at tw host drain, then --force; got %#v %v", ce, err)
	}
	for _, c := range []struct{ host, flag string }{{"h1", "--force"}, {"h2", "--drain"}} {
		out, err := run("rm", c.host, c.flag, "--yes")
		if err != nil || !strings.Contains(out, "host     "+c.host+" removed") || !strings.Contains(out, "1 leases released") {
			t.Fatalf("host rm %s %s: %q %v", c.host, c.flag, out, err)
		}
	}
}
