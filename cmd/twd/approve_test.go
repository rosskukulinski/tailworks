package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/twd/internal/approvecap"
	"github.com/rosskukulinski/twd/mgmt"
)

// `twd approvals` and `twd approve` on a linked machine: the CLI asks the
// linked mgmt (a real twd-mgmt behind its WhoIs gate; this machine holds
// twd.dev/cap/approve), which reads and forwards to the lease holder's
// hostd (a fake push listener that records the decision).
func TestApproveCLI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twdappr-")
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
	os.WriteFile(keyFile, []byte("TS_API_KEY=fake-not-a-key\n"), 0o600)
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, Broker: &mgmt.Broker{BaseURL: "http://tsapi.invalid"},
		IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Hour, PolicyMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.CreateTenant(ctx, mgmt.TenantRequest{ID: "tn_a", Tailnet: "a.ts.net", MintMode: mgmt.MintAPIKey, APIKeyFile: keyFile}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var decided []map[string]any
	at := time.Now().Add(-90 * time.Minute).UTC()
	sock := filepath.Join(dir, "hostd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hsrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/approvals":
			json.NewEncoder(w).Encode(map[string]any{"approvals": []map[string]any{
				{"tenantID": "tn_a", "workload": "refunds", "workflow": "refund", "instance": "wf_1", "step": "wait-for-approval", "type": "approval", "status": "pending", "requestedAt": at, "facts": map[string]float64{"amount": 42}},
			}})
		case "/v1/approvals/decide":
			var d map[string]any
			json.NewDecoder(r.Body).Decode(&d)
			mu.Lock()
			decided = append(decided, d)
			mu.Unlock()
			status := "approved"
			if d["approve"] != true {
				status = "rejected"
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "approval": map[string]any{"tenantID": "tn_a", "workload": "refunds", "workflow": d["workflow"], "instance": d["instance"],
				"step": "wait-for-approval", "type": "approval", "status": status, "requestedAt": at, "approver": d["approver"], "comment": d["comment"]}})
		}
	})}
	go hsrv.Serve(ln)
	t.Cleanup(func() { hsrv.Close() })
	if _, _, err := m.RegisterHost(ctx, mgmt.RegisterRequest{HostID: "h1", CapacityNodes: 10, Tiers: []string{"process"}, PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcquireLease(ctx, mgmt.LeaseName("tn_a", "refunds"), "h1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := &mgmt.Server{M: m}
	api := httptest.NewServer(srv.TailnetHandler(func(context.Context, string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nBOB", Name: "bob-laptop.a.ts.net.", Login: "bob@example.com", Caps: []string{approvecap.Cap},
			CapValues: map[string][]json.RawMessage{approvecap.Cap: {json.RawMessage(`{"workflows":["refund"]}`)}}}, nil
	}, srv.Handler()))
	t.Cleanup(api.Close)
	devDir := filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o700)
	os.WriteFile(filepath.Join(devDir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-14T00:00:00Z"}`), 0o600)
	run := func(cmd func([]string) error, args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		err := cmd(append([]string{"--state-dir", devDir}, args...))
		w.Close()
		os.Stdout = old
		return <-done, err
	}
	// Without --tenant (or with the tailnet's name): whoami's tenant, the
	// one on this machine's tailnet, though the approver can't list tenants.
	for _, extra := range [][]string{nil, {"--tenant", "a.ts.net"}, {"--tenant", "a"}} {
		if out, err := run(cmdApprovals, extra...); err != nil || !strings.Contains(out, "wf_1") {
			t.Fatalf("approvals %v: %q %v", extra, out, err)
		}
	}
	plain := run
	run = func(cmd func([]string) error, args ...string) (string, error) {
		t.Helper()
		return plain(cmd, append([]string{"--tenant", "tn_a"}, args...)...)
	}

	out, err := run(cmdApprovals)
	if err != nil || !strings.Contains(out, "refunds") || !strings.Contains(out, "wf_1") || !strings.Contains(out, "amount=42") || !strings.Contains(out, "1h") {
		t.Fatalf("approvals: %q %v", out, err)
	}
	out, err = run(cmdApprove, "refunds", "wf_1", "--comment", "looks right")
	if err != nil || !strings.Contains(out, "✓ approved refunds/refund/wf_1") || !strings.Contains(out, "bob@example.com") {
		t.Fatalf("approve: %q %v", out, err)
	}
	out, err = run(cmdApprove, "refunds", "wf_1", "--reject", "--workflow", "refund")
	if err != nil || !strings.Contains(out, "rejected") {
		t.Fatalf("reject: %q %v", out, err)
	}
	mu.Lock()
	if len(decided) != 2 || decided[0]["approve"] != true || decided[0]["comment"] != "looks right" || decided[1]["approve"] != false ||
		decided[0]["approver"].(map[string]any)["nodeId"] != "nBOB" {
		t.Fatalf("decisions at the host: %v", decided)
	}
	mu.Unlock()
	if _, err := run(cmdApprove, "refunds", "wf_nope"); cliCode(err) != "approval_not_pending" {
		t.Fatalf("unknown instance: %v", err)
	}
	if _, err := run(cmdApprove, "refunds"); cliCode(err) != "bad_request" {
		t.Fatalf("missing instance: %v", err)
	}
}
