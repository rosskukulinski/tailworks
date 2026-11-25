//go:build e2edataplane

// Package dataplane is the Jepsen-style chaos loop for the data plane
// (issue #415, LOOP.md): real tw-mgmt and three real tw-hostd processes on
// testcontrol + a local DERP, one stateful workload (testdata/dp) that uses
// Queues, Durable Objects, the lock template and D1, a seeded nemesis, and
// checkers over a recorded history. Nothing talks to the internet; no real
// keys.
package dataplane

import (
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const fakeToken = "fake-api-token"

// fakeTS is the smallest Tailscale API mgmt needs (as in e2e/queues).
func fakeTS(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" {
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

// sockProxy forwards a unix socket to mgmt's; cut(true) drops every
// connection and refuses new ones (the host is cut off from mgmt but stays
// on the tailnet), as in e2e/locks.
type sockProxy struct {
	target string
	mu     sync.Mutex
	cutOff bool
	conns  map[net.Conn]bool
}

func startProxy(t *testing.T, path, target string) *sockProxy {
	p := &sockProxy{target: target, conns: map[net.Conn]bool{}}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.cutOff {
				p.mu.Unlock()
				c.Close()
				continue
			}
			u, err := net.Dial("unix", target)
			if err != nil {
				p.mu.Unlock()
				c.Close()
				continue
			}
			p.conns[c], p.conns[u] = true, true
			p.mu.Unlock()
			go func() { io.Copy(u, c); u.Close() }()
			go func() { io.Copy(c, u); c.Close() }()
		}
	}()
	return p
}

func (p *sockProxy) cut(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cutOff = on
	if on {
		for c := range p.conns {
			c.Close()
		}
		p.conns = map[net.Conn]bool{}
	}
}

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// cluster is mgmt plus the hosts. Every process it starts is recorded in
// <run>/pids; it only ever signals those (and their process groups).
type cluster struct {
	t        *testing.T
	bin, dir string // binaries; run directory (artifacts)
	work     string // short dir for sockets and state
	ctl      *nettest.Control
	tsURL    string
	sock     string
	workerd  string
	hosts    []string
	proxies  map[string]*sockProxy
	hc       *http.Client
	pids     *os.File
	tenantID string

	// pg, when set (TW_DP_MGMT=pg3), runs mgmt as replicas on Postgres
	// (mgmtpg_test.go) instead of one tw-mgmt on SQLite.
	pg *mgmtPG

	// workload is the workload deploy names ("dp" if empty).
	workload string

	// hostArgs adds flags to a host's tw-hostd (the R2 backend: r2_test.go).
	hostArgs func(id string) []string

	mu      sync.Mutex
	procs   map[string]*proc
	token   string
	stopped map[string]bool // names currently SIGSTOPped (whole group or hostd only)
	orphans []int           // workerds that outlived a crashed hostd

	clocks *hostClocks // skew_test.go
}

func (c *cluster) record(name string, pid int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.pids, "%s %s %d\n", time.Now().UTC().Format(time.RFC3339Nano), name, pid)
}

func (c *cluster) start(name string, args ...string) {
	c.t.Helper()
	lf, err := os.OpenFile(filepath.Join(c.dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		c.t.Fatal(err)
	}
	bin := "tw-hostd"
	if strings.HasPrefix(name, "mgmt") { // "mgmt", or "mgmt-r<i>" (mgmtpg_test.go)
		bin = "tw-mgmt"
	}
	cmd := exec.Command(filepath.Join(c.bin, bin), args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), c.clockEnv(name)...) // skew_test.go
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	fmt.Fprintf(lf, "==== %s started pid %d\n", time.Now().UTC().Format(time.RFC3339Nano), cmd.Process.Pid)
	c.record(name, cmd.Process.Pid)
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); lf.Close(); close(p.done) }()
	c.mu.Lock()
	c.procs[name] = p
	c.mu.Unlock()
}

func (c *cluster) pid(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.procs[name]; p != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

// descendants are pid's child processes, recursively. hostd starts each
// workerd in a process group of its own (Setpgid), so a signal to hostd's
// group doesn't reach them; a host-wide fault signals these too.
func descendants(pid int) []int {
	var out []int
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		b, _ := exec.Command("pgrep", "-P", strconv.Itoa(p)).Output()
		for _, f := range strings.Fields(string(b)) {
			if cp, err := strconv.Atoi(f); err == nil {
				out = append(out, cp)
				queue = append(queue, cp)
			}
		}
	}
	return out
}

func comm(pid int) string {
	b, _ := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(b))
}

// signalTree sends sig to pid's group and to each of its descendants.
func (c *cluster) signalTree(name string, pid int, sig syscall.Signal) {
	kids := descendants(pid)
	syscall.Kill(-pid, sig)
	for _, k := range kids {
		syscall.Kill(k, sig)
	}
	if sig == syscall.SIGKILL || sig == syscall.SIGSTOP {
		for _, k := range kids {
			c.record(fmt.Sprintf("%s/%s(%v)", name, filepath.Base(comm(k)), sig), k)
		}
	}
}

// stop ends a process, its group and its descendants (workerd): SIGTERM
// to hostd (which stops its workerds), or SIGKILL to all of them at once
// when crash.
func (c *cluster) stop(name string, crash bool) {
	c.mu.Lock()
	p := c.procs[name]
	delete(c.procs, name)
	wasStopped := c.stopped[name]
	delete(c.stopped, name)
	c.mu.Unlock()
	if p == nil {
		return
	}
	pid := p.cmd.Process.Pid
	kids := descendants(pid)
	if crash {
		c.signalTree(name, pid, syscall.SIGKILL)
	} else {
		if wasStopped {
			c.signalTree(name, pid, syscall.SIGCONT)
		}
		syscall.Kill(-pid, syscall.SIGTERM)
	}
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		syscall.Kill(-pid, syscall.SIGKILL)
		<-p.done
	}
	// Whatever hostd left behind (it normally stops its workerds itself).
	for _, k := range kids {
		syscall.Kill(k, syscall.SIGKILL)
	}
}

// crashOnly SIGKILLs the hostd process alone (not its workerds) and
// forgets it, as a crash would leave things.
func (c *cluster) crashOnly(name string) {
	c.mu.Lock()
	p := c.procs[name]
	delete(c.procs, name)
	delete(c.stopped, name)
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.record(name+"(crash)", p.cmd.Process.Pid)
	syscall.Kill(p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
}

// orphan records a process the harness started (indirectly) that outlived
// its hostd; stopAll kills it.
func (c *cluster) orphan(name string, pid int) {
	c.record(name+"/orphan", pid)
	c.mu.Lock()
	c.orphans = append(c.orphans, pid)
	c.mu.Unlock()
}

// pause SIGSTOPs name: hostd and every workerd (the whole host stalls), or
// only the hostd process when hostdOnly (workerd keeps running). resume
// SIGCONTs all of them.
func (c *cluster) pause(name string, hostdOnly bool) {
	pid := c.pid(name)
	if pid == 0 {
		return
	}
	if hostdOnly {
		syscall.Kill(pid, syscall.SIGSTOP)
	} else {
		c.signalTree(name, pid, syscall.SIGSTOP)
	}
	c.mu.Lock()
	c.stopped[name] = true
	c.mu.Unlock()
}

func (c *cluster) resume(name string) {
	pid := c.pid(name)
	if pid == 0 {
		return
	}
	c.signalTree(name, pid, syscall.SIGCONT)
	c.mu.Lock()
	delete(c.stopped, name)
	c.mu.Unlock()
}

// killWorkerd SIGKILLs the workerd processes under name's hostd (hostd
// survives and restarts them).
func (c *cluster) killWorkerd(name string) int {
	pid := c.pid(name)
	if pid == 0 {
		return 0
	}
	n := 0
	for _, k := range descendants(pid) {
		if !strings.Contains(comm(k), "workerd") {
			continue
		}
		c.record(name+"/workerd(kill)", k)
		syscall.Kill(k, syscall.SIGKILL)
		n++
	}
	return n
}

func (c *cluster) stopAll() {
	c.mu.Lock()
	names := make([]string, 0, len(c.procs))
	for n := range c.procs {
		names = append(names, n)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func() { defer wg.Done(); c.stop(n, false) }()
	}
	wg.Wait()
	c.mu.Lock()
	for _, p := range c.orphans {
		syscall.Kill(p, syscall.SIGKILL)
	}
	c.mu.Unlock()
}

func (c *cluster) startMgmt(first bool) {
	if c.usePG(first) {
		c.startMgmtPG(first) // mgmtpg_test.go
		return
	}
	args := []string{"-local-dev", "-socket", c.sock, "-host-token-file", filepath.Join(c.work, "token"),
		"-state-dir", filepath.Join(c.work, "mgmt"), "-tsapi-url", c.tsURL, "-reap-interval", "1h"}
	if first {
		args = append(args, "-rotate-host-token")
		os.Remove(filepath.Join(c.work, "token"))
	}
	c.start("mgmt", args...)
	for i := 0; ; i++ {
		b, err := os.ReadFile(filepath.Join(c.work, "token"))
		if err == nil && len(bytes.TrimSpace(b)) == 64 {
			if st, _, err := c.try("GET", "/v1/hosts", nil, nil); err == nil && st == 200 {
				c.mu.Lock()
				c.token = string(bytes.TrimSpace(b))
				c.mu.Unlock()
				return
			}
			c.mu.Lock()
			c.token = string(bytes.TrimSpace(b))
			c.mu.Unlock()
		}
		if i > 300 {
			c.dumpLogs()
			c.t.Fatal("mgmt did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (c *cluster) startHost(id string) {
	hd := filepath.Join(c.work, id)
	args := []string{"-local-dev", "-mgmt", "unix://" + filepath.Join(c.work, id+"-mgmt.sock"), "-mgmt-token-file", filepath.Join(c.work, "token"),
		"-host-id", id, "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
		"-control-url", c.ctl.URL, "-ephemeral", "-queue-visibility", "5s",
		"-sandbox", "none", "-workerd", c.workerd, "-hostname-prefix", id + "-", "-log-level", "debug"}
	if c.hostArgs != nil {
		args = append(args, c.hostArgs(id)...)
	}
	c.start(id, args...)
}

// try is one mgmt API call; err is a transport failure.
func (c *cluster) try(method, path string, body, out any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, "http://mgmt"+path, rd)
	c.mu.Lock()
	rq.Header.Set(mgmt.HostTokenHeader, c.token)
	c.mu.Unlock()
	rq.Header.Set("content-type", "application/json")
	resp, err := c.hc.Do(rq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		json.Unmarshal(b, out)
	}
	return resp.StatusCode, b, nil
}

// must retries transport errors (mgmt may be restarting) for up to 30 s.
func (c *cluster) must(method, path string, body, out any) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, b, err := c.try(method, path, body, out)
		if err == nil && st/100 == 2 {
			return
		}
		if err == nil || time.Now().After(deadline) {
			c.dumpLogs()
			c.t.Fatalf("%s %s: %d %s %v", method, path, st, b, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (c *cluster) dumpLogs() {
	for _, name := range append([]string{"mgmt"}, c.hosts...) {
		b, _ := os.ReadFile(filepath.Join(c.dir, name+".log"))
		lines := strings.Split(string(b), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		c.t.Logf("---- %s.log (tail)\n%s", name, strings.Join(lines, "\n"))
	}
}

type deployment struct {
	DeploymentID, Host, Status, Error string
	Epoch                             int64
	Endpoint                          struct{ MagicDNS, Status string }
}

func (c *cluster) waitActive(id string, timeout time.Duration) (deployment, error) {
	deadline := time.Now().Add(timeout)
	for {
		var d deployment
		st, b, err := c.try("GET", "/v1/deployments/"+id, nil, &d)
		if err == nil && st != 200 {
			return d, fmt.Errorf("deployment %s: %d %s", id, st, b)
		}
		if err == nil && d.Status == "active" && d.Endpoint.Status == "ready" && strings.HasPrefix(d.Endpoint.MagicDNS, d.Host+"-") {
			return d, nil
		}
		if d.Status == "failed" || time.Now().After(deadline) {
			return d, fmt.Errorf("deployment %s: status %s endpoint %s: %s (%v)", id, d.Status, d.Endpoint.Status, d.Error, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (c *cluster) waitHostsLive(ids ...string) {
	c.t.Helper()
	for i := 0; ; i++ {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		c.must("GET", "/v1/hosts", nil, &pg)
		live := 0
		for _, h := range pg.Items {
			for _, id := range ids {
				if h.HostID == id && h.Status == "live" {
					live++
				}
			}
		}
		if live == len(ids) {
			return
		}
		if i > 600 {
			c.dumpLogs()
			c.t.Fatalf("hosts %v not live: %+v", ids, pg.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// deploy posts one deployment of the dp workload; st is the HTTP status.
func (c *cluster) deploy(bundle, host string, replace, force bool) (deployment, int, []byte, error) {
	var d deployment
	wl := c.workload
	if wl == "" {
		wl = "dp"
	}
	st, b, err := c.try("POST", "/v1/deployments", map[string]any{"tenantID": c.tenantID, "workload": wl, "bundle": bundle,
		"host": host, "replace": replace, "forceTakeover": force}, &d)
	return d, st, b, err
}

func (c *cluster) queueBacklog(q string) (int, error) {
	var qi mgmt.QueueInfo
	st, b, err := c.try("GET", "/v1/tenants/"+c.tenantID+"/queues/"+q, nil, &qi)
	if err != nil {
		return 0, err
	}
	if st == 404 {
		return 0, nil
	}
	if st != 200 {
		return 0, fmt.Errorf("%d %s", st, b)
	}
	return int(qi.Backlog), nil
}

// writeJSON writes v to the run directory.
func writeJSON(dir, name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(filepath.Join(dir, name), b, 0o600)
}

func ctxTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
