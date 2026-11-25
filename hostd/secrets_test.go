package hostd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// The secret value of these tests: quotes, a backslash and a newline must
// survive the Cap'n Proto text literal and the JSON of tw-config.json.
const testSecret = "sk-test-\"quoted\" \\ back\nline2"

func putSecret(t *testing.T, e *hostdtest.Env, workload, name, value string) {
	t.Helper()
	if err := e.Mgmt.Do(context.Background(), "PUT", "/v1/tenants/"+e.TenantID+"/workloads/"+workload+"/secrets/"+name,
		map[string]string{"value": value}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerSecrets: secrets set in mgmt reach the workload as text
// bindings (env.NAME is a string) in the worker and in a facet Durable
// Object, override a wrangler var of the same name, are fetched once per
// deploy through the lease-fenced grant, and are written only inside the
// instance directory.
func TestWorkerSecrets(t *testing.T) {
	t.Parallel()
	testWorkerSecrets(t, hostdtest.BackendFake)
}

// testWorkerSecrets is TestWorkerSecrets against backend (realmgmt_test.go).
func testWorkerSecrets(t *testing.T, backend string) {
	s := startSuiteBackend(t, fakemgmt.Config{}, backend)
	e := s.e
	putSecret(t, e, "secrets", "API_KEY", testSecret)
	putSecret(t, e, "secrets", "MULTI", "two")
	dep := e.Deploy(hostdtest.Fixture("secrets"), "secrets", nil)
	wl := e.WaitActive("secrets", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	if n := e.Hooks.SecretGrants(); n != 1 {
		t.Fatalf("secret grants %d, want 1 (one per deploy)", n)
	}
	check := func(path string) {
		t.Helper()
		code, _, b := s.do("GET", "http://"+wl.IP+path, nil)
		var got map[string]any
		if code != 200 || json.Unmarshal(b, &got) != nil {
			t.Fatalf("%s: %d %s", path, code, b)
		}
		if got["apiKey"] != testSecret || got["type"] != "string" || got["multi"] != "two" || got["greeting"] != "from-vars" {
			t.Fatalf("%s: env %v", path, got)
		}
	}
	check("/")   // the tenant worker's text bindings
	check("/do") // a facet's env (tw-config.json)

	// The value is written only into the instance directory (config.capnp
	// as a text binding, tw-config.json for facets), nowhere else under
	// hostd's data or run directories.
	caps, _ := filepath.Glob(filepath.Join(e.Dir, "run", "i*", "config.capnp"))
	if len(caps) == 0 {
		t.Fatal("no instance config.capnp")
	}
	found := false
	for _, p := range caps {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), `(name = "API_KEY", text = "sk-test-\"quoted\" \\ back\nline2")`) {
			found = true
			if strings.Contains(string(b), "the-var-a-secret-overrides") {
				t.Fatal("config.capnp still binds the var the secret overrides")
			}
		}
	}
	if !found {
		t.Fatal("no text binding for API_KEY in any instance config.capnp")
	}
	needle := "sk-test-"
	filepath.WalkDir(e.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeType != 0 {
			return nil
		}
		rel, _ := filepath.Rel(e.Dir, p)
		inInstance := strings.HasPrefix(rel, "run"+string(filepath.Separator)+"i")
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), needle) && !inInstance {
			t.Errorf("secret value found outside the instance directory: %s", rel)
		}
		return nil
	})
	for _, w := range e.Host.Status().Workloads {
		if strings.Contains(w.Error, needle) {
			t.Fatalf("status error carries the value: %s", w.Error)
		}
	}

	// Updating a secret takes effect on the next deploy.
	putSecret(t, e, "secrets", "MULTI", "three")
	dep2 := e.Deploy(hostdtest.Fixture("secrets"), "secrets", nil)
	e.WaitActive("secrets", dep2, 60*time.Second)
	code, _, b := s.do("GET", "http://"+wl.IP+"/", nil)
	if code != 200 || !strings.Contains(string(b), `"multi":"three"`) {
		t.Fatalf("after redeploy: %d %s", code, b)
	}
}

// TestNodeSecrets: for runtime "node" a secret is in process.env and
// overrides node.env of the same name, but it is NOT in the process's
// exec-time environment (on macOS any same-user process can read that), and
// the preload deletes its file once it has read it.
func TestNodeSecrets(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := os.Stat(hostdtest.NodeDeps()); err != nil {
		t.Skip("node tier deps not installed (make test-node)")
	}
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	putSecret(t, e, "nodeprobe", "PROBE_GREETING", "from-a-secret")
	dep := e.DeployNode(hostdtest.Fixture("nodeprobe"), "nodeprobe", nil)
	wl := e.WaitActive("nodeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	code, _, b := s.do("GET", "http://"+wl.IP+"/whoami", nil)
	if code != 200 || !strings.Contains(string(b), `"greeting":"from-a-secret"`) {
		t.Fatalf("whoami: %d %s", code, b)
	}
	var pid int
	var dir string
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "nodeprobe" && w.PID > 0 {
			pid, dir = w.PID, w.InstanceDir
		}
	}
	if pid == 0 {
		t.Fatal("no node pid in status")
	}
	pe, err := procEnv(pid)
	if err != nil {
		t.Fatalf("read pid %d env: %v", pid, err)
	}
	if bytes.Contains(pe, []byte("from-a-secret")) {
		t.Fatalf("the secret is in node's exec-time environment (pid %d)", pid)
	}
	if !bytes.Contains(pe, []byte("TW_WORKLOAD=")) {
		t.Fatalf("procEnv read no environment for pid %d: the check above proves nothing", pid)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tailworks-secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("secrets file still in the instance dir after start: %v", err)
	}
}
