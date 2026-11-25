//go:build e2eupgrade

// Package upgrade is the release gate for upgrades
// (ADR-041 §9): the previous release's tw-mgmt and
// tw-hostd, real processes on testcontrol (hermetic), serve a workload
// with Durable Object storage; then mgmt and the host are replaced by the
// candidate, in order, on the same state, and the test checks nothing was
// lost: DO storage, the workload's node identity (rejoined, never
// re-registered), the store's migrations; that the previous mgmt still
// opens the expanded store (expand-only migrations); and that a host
// outside the version window is refused.
//
// TW_UPGRADE_FROM names the previous release: a git ref (default, from
// the Makefile: the newest published GitHub release, scripts/upgrade-from.sh)
// built in a temporary git worktree, or "self" (this tree stamped TW_UPGRADE_FROM_VERSION, a fast
// local check of the machinery).
package upgrade

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const fakeToken = "tskey-api-upgrade-test"

var _ = bufio.NewReader

func fakeTS(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" { // the OAuth client's exchange (mint mode oauth)
			json.NewEncoder(w).Encode(map[string]any{"access_token": fakeToken, "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != fakeToken {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
			mu.Lock()
			n++
			id := fmt.Sprintf("k%06d", n)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "fake-authkey-" + id})
		case r.Method == "DELETE":
			w.WriteHeader(200)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
			w.Header().Set("ETag", `"p1"`)
			w.Write([]byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
			json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type proc struct {
	name string
	cmd  *exec.Cmd
	log  string
	done chan struct{}
}

type env struct {
	t        *testing.T
	bin, dir string
	// binOf picks a process's binary directory (old or new release).
	binOf   map[string]string
	ctl     *nettest.Control
	sock    string
	token   string
	hc      *http.Client
	mu      sync.Mutex
	procs   map[string]*proc
	workerd string
}

func (e *env) start(name string, extraEnv []string, args ...string) *proc {
	e.t.Helper()
	logf := filepath.Join(e.dir, name+".log")
	lf, err := os.OpenFile(logf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	bin := "tw-hostd"
	if name == "mgmt" {
		bin = "tw-mgmt"
	}
	dir := e.bin
	if d, ok := e.binOf[name]; ok {
		dir = d
	}
	cmd := exec.Command(filepath.Join(dir, bin), args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	p := &proc{name: name, cmd: cmd, log: logf, done: make(chan struct{})}
	go func() { cmd.Wait(); lf.Close(); close(p.done) }()
	e.mu.Lock()
	e.procs[name] = p
	e.mu.Unlock()
	return p
}

// stop terminates a process this test started (and its process group:
// workerd children), waiting for it to exit.
func (e *env) stop(name string) {
	e.mu.Lock()
	p := e.procs[name]
	delete(e.procs, name)
	e.mu.Unlock()
	if p == nil {
		return
	}
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

func (e *env) startHost(id string, forceDERP bool) {
	// Not -ephemeral: a restart must rejoin from saved node state.
	var envv []string
	if forceDERP {
		envv = append(envv, "TS_DEBUG_ALWAYS_USE_DERP=1")
	}
	hd := filepath.Join(e.dir, id)
	e.start(id, envv, "-local-dev", "-mgmt", "unix://"+e.sock, "-mgmt-token-file", filepath.Join(e.dir, "token"),
		"-host-id", id, "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
		"-control-url", e.ctl.URL, "-heartbeat", "500ms", "-path-every", "2s",
		"-sandbox", "none", "-workerd", e.workerd, "-hostname-prefix", id+"-", "-log-level", "debug")
}

func (e *env) do(method, path string, body, out any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, "http://mgmt"+path, rd)
	rq.Header.Set(mgmt.HostTokenHeader, e.token)
	rq.Header.Set("content-type", "application/json")
	resp, err := e.hc.Do(rq)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		json.Unmarshal(b, out)
	}
	return resp.StatusCode, b
}

func (e *env) must(method, path string, body, out any) {
	e.t.Helper()
	if st, b := e.do(method, path, body, out); st/100 != 2 {
		e.t.Fatalf("%s %s: %d %s", method, path, st, b)
	}
}

func (e *env) dumpLogs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, name := range []string{"mgmt", "h1", "h2"} {
		b, _ := os.ReadFile(filepath.Join(e.dir, name+".log"))
		lines := strings.Split(string(b), "\n")
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		e.t.Logf("---- %s.log (tail)\n%s", name, strings.Join(lines, "\n"))
	}
}

type deployment struct {
	DeploymentID, Host, Status, Error string
	Epoch                             int64
	Placement                         mgmt.PlacementDecision
	Endpoint                          struct{ Node, MagicDNS, Status string }
}

func (e *env) waitActive(id string, timeout time.Duration) deployment {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var d deployment
		e.must("GET", "/v1/deployments/"+id, nil, &d)
		// The entry node must be the one on the deployment's host (after a
		// move the object briefly still names the old host's node).
		if d.Status == "active" && d.Endpoint.Status == "ready" && strings.HasPrefix(d.Endpoint.MagicDNS, d.Host+"-") {
			return d
		}
		if d.Status == "failed" || time.Now().After(deadline) {
			e.dumpLogs()
			e.t.Fatalf("deployment %s: status %s endpoint %s: %s", id, d.Status, d.Endpoint.Status, d.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// hostStatus reads a hostd's local status (its push socket).
func (e *env) hostWorkloads(id string) map[string]string {
	sock := filepath.Join(e.dir, id, "run", "hostd.sock")
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Get("http://hostd/v1/status")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var st struct {
		Workloads []struct{ Workload, Status string }
	}
	json.NewDecoder(resp.Body).Decode(&st)
	out := map[string]string{}
	for _, w := range st.Workloads {
		out[w.Workload] = w.Status
	}
	return out
}
