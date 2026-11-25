//go:build e2evm && (linux || darwin)

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// vmSuite is one hermetic microVM-tier host (testcontrol, fakemgmt,
// tw-hostd with the VM backend, the tenant's tier "microvm") and a tailnet
// client, for the per-feature tests (d1_test.go, queues_test.go, ...).
type vmSuite struct {
	t      *testing.T
	e      *hostdtest.Env
	client *nettest.Node
	hc     *http.Client

	// ctlURL is the testcontrol's URL, for more tailnet nodes.
	ctlURL string
}

// startVMSuite skips without the guest assets; opt adjusts the hostd
// options (cron clock, queue visibility, ...).
func startVMSuite(t *testing.T, opt func(*hostdtest.Options)) *vmSuite {
	t.Helper()
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL and TW_VM_ROOTFS must be set (make e2e-vm / e2e-vz)")
	}
	be, _, err := newBackend(kernel, rootfs, 2, -1, false)
	if err != nil {
		t.Fatalf("microVM backend unavailable: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	o := hostdtest.Options{ControlURL: ctl.URL, Fake: fakeStatic(), Ephemeral: true, KillTombstone: 5 * time.Second,
		VM:     be,
		Tenant: mgmtapi.Tenant{Tailnet: "ttest.ts.net", ControlURL: ctl.URL, MintMode: "static", Tier: bundle.TierMicroVM}}
	if opt != nil {
		opt(&o)
	}
	e := hostdtest.Start(t, o)
	client, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return &vmSuite{t: t, e: e, client: client, hc: client.HTTPClient(60 * time.Second), ctlURL: ctl.URL}
}

// vmApp returns the path of an e2e/vm test app.
func vmApp(name string) string {
	root, _ := nettest.FindRepoRoot()
	return filepath.Join(root, "e2e", "vm", "testdata", name)
}

// deploy deploys the app in dir as workload, waits for it to be active in
// a microVM and reachable, and returns "http://<workload IP>".
func (s *vmSuite) deploy(dir, workload string, vars map[string]string) string {
	s.t.Helper()
	dep := s.e.Deploy(dir, workload, vars)
	return s.waitActive(workload, dep)
}

func (s *vmSuite) waitActive(workload, dep string) string {
	s.t.Helper()
	wl := s.e.WaitActive(workload, dep, 3*time.Minute)
	for _, w := range s.e.Host.Status().Workloads {
		if w.Workload == workload && w.Tier != bundle.TierMicroVM {
			s.t.Fatalf("%s runs in tier %q, want microvm", workload, w.Tier)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.client.WaitPeers(ctx, wl.IP); err != nil {
		s.t.Fatal(err)
	}
	return "http://" + wl.IP
}

// do sends a request and returns the status and the body decoded as JSON
// (any JSON value; a non-JSON body is returned as {"raw": body}). A
// transport error is status 0.
func (s *vmSuite) do(method, url string, body any) (int, any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, map[string]any{"err": err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var v any
	if json.Unmarshal(b, &v) != nil {
		v = map[string]any{"raw": string(b)}
	}
	return resp.StatusCode, v
}

// must is do that fails the test unless the status is 200 and the body a
// JSON object without "error"; it returns the object.
func (s *vmSuite) must(method, url string, body any) map[string]any {
	s.t.Helper()
	code, v := s.do(method, url, body)
	m, _ := v.(map[string]any)
	if code != 200 || m == nil || m["error"] != nil {
		s.t.Fatalf("%s %s: %d %v", method, url, code, v)
	}
	return m
}

// eventually polls f every 100 ms until it returns true or timeout passes.
func (s *vmSuite) eventually(timeout time.Duration, what string, f func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for !f() {
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: not within %v", what, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// killVM SIGKILLs the workload's VMM (a guest crash) and waits until hostd
// has started a new VM and ready (a GET of readyURL returns 200).
func (s *vmSuite) killVM(workload, readyURL string) {
	s.t.Helper()
	old := s.e.Host.Sandbox(s.e.TenantID, workload)
	if old == nil {
		s.t.Fatalf("%s: no VM", workload)
	}
	st := time.Now()
	old.Kill()
	s.eventually(2*time.Minute, workload+": a new VM serving after the kill", func() bool {
		nv := s.e.Host.Sandbox(s.e.TenantID, workload)
		if nv == nil || nv == old {
			return false
		}
		code, _ := s.do("GET", readyURL, nil)
		return code == 200
	})
	s.t.Logf("%s: VM killed; a new VM served %v later", workload, time.Since(st).Round(time.Millisecond))
}

// redeploy deploys dir again (a new VM on the same volume) and waits for it.
func (s *vmSuite) redeploy(dir, workload string) {
	s.t.Helper()
	old := s.e.Host.Sandbox(s.e.TenantID, workload)
	s.waitActive(workload, s.e.Deploy(dir, workload, nil))
	if nv := s.e.Host.Sandbox(s.e.TenantID, workload); nv == nil || nv == old {
		s.t.Fatalf("%s: the redeploy did not replace the VM", workload)
	}
}
