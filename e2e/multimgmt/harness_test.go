//go:build e2emultimgmt

// Package multimgmt is the hermetic multi-replica tw-mgmt harness driven by
// e2e/multimgmt/LOOP.md: N real tw-mgmt processes sharing one Postgres,
// a fake Tailscale API and a fake setec. Nothing here touches a real tailnet.
package multimgmt

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
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

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/e2e/internal/mgmtpg"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// pgContainer is the throwaway Postgres container's name. TW_MM_PG_NAME
// (with TW_MM_PG_PORT) isolates concurrent runs from other worktrees,
// which would otherwise remove each other's container mid-scenario.
var pgContainer = env("TW_MM_PG_NAME", "tw-mm-pg")

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---- run directory and process records ----

type run struct {
	t    *testing.T
	dir  string
	pids *os.File
	mu   sync.Mutex
}

func newRun(t *testing.T, item string) *run {
	t.Helper()
	root, _ := filepath.Abs("results")
	dir := filepath.Join(root, time.Now().UTC().Format("20060102T150405Z")+"-"+item)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "pids"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	t.Logf("artifacts: %s", dir)
	return &run{t: t, dir: dir, pids: f}
}

func (r *run) record(kind, name string, pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.pids, "%s %s %d\n", kind, name, pid)
}

// ---- Postgres (OrbStack docker) ----

// startPG starts a fresh tw-mm-pg container (or uses TW_MM_PG_DSN) and
// returns the DSN. The container is removed when the test ends.
func startPG(t *testing.T, r *run) string {
	t.Helper()
	if dsn := os.Getenv("TW_MM_PG_DSN"); dsn != "" {
		return dsn
	}
	return mgmtpg.Start(t, mgmtpg.Options{Name: pgContainer, Port: env("TW_MM_PG_PORT", "55434"), Ctx: env("PG_CTX", "orbstack"),
		LogDir: r.dir, Record: r.record}).DSN
}

// ---- fake Tailscale API ----

// fakeTS is the subset of the Tailscale API that mgmt's mint broker and
// device deletes use. It counts keys per object ("tw <objectID>").
type fakeTS struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
	// keysByObject: object ID → key IDs ever created for it.
	keysByObject map[string][]string
	liveKeys     map[string]string // key ID → object ID ("pool:<key ID>" for warm-pool keys)
	keyTags      map[string]string // key ID → its tags (sorted, comma-joined) + "|ephemeral" / "|persistent"
	createTimes  []time.Time
	deletedDevs  []string
	hold         bool // while true, key creation answers 503 (objects stay pending)
	held         int  // 503s served while holding
}

func (f *fakeTS) setHold(v bool) { f.mu.Lock(); f.hold = v; f.mu.Unlock() }

// keyCounts returns object ID → number of keys ever created for it.
func (f *fakeTS) keyCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for o, ks := range f.keysByObject {
		out[o] = len(ks)
	}
	return out
}

// liveKeyCounts returns object ID → number of keys created and not deleted.
func (f *fakeTS) liveKeyCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, o := range f.liveKeys {
		out[o]++
	}
	return out
}

const fakeAPIToken = "fake-api-token"

// fakePolicy lets SupportAgent objects spawn LookupAgent children.
const fakePolicy = `{
  "tagOwners": {
    "tag:tw": ["autogroup:admin"],
    "tag:tw-agent-support": ["tag:tw"],
    "tag:tw-agent-support-sub": ["tag:tw", "tag:tw-agent-support"],
  },
}`

func newFakeTS(t *testing.T) *fakeTS {
	f := &fakeTS{keysByObject: map[string][]string{}, liveKeys: map[string]string{}, keyTags: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTS) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v2/oauth/token" { // the OAuth client's exchange (mint mode oauth)
		json.NewEncoder(w).Encode(map[string]any{"access_token": fakeAPIToken, "token_type": "Bearer", "expires_in": 3600})
		return
	}
	if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != fakeAPIToken {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"invalid token"}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys") && f.hold:
		f.held++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
		w.Write([]byte(`{"message":"fakeTS: holding"}`))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
		var body struct {
			Description  string `json:"description"`
			Capabilities struct {
				Devices struct {
					Create struct {
						Ephemeral bool     `json:"ephemeral"`
						Tags      []string `json:"tags"`
					} `json:"create"`
				} `json:"devices"`
			} `json:"capabilities"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.n++
		id := fmt.Sprintf("k%06d", f.n)
		obj := strings.TrimPrefix(body.Description, "tw ")
		if strings.HasPrefix(body.Description, "tw pool ") {
			obj = "pool:" + id // a warm key belongs to no object until one adopts it
		}
		cr := body.Capabilities.Devices.Create
		f.keyTags[id] = mgmt.PoolName(cr.Tags, cr.Ephemeral)
		f.keysByObject[obj] = append(f.keysByObject[obj], id)
		f.liveKeys[id] = obj
		f.createTimes = append(f.createTimes, time.Now())
		json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "fake-authkey-" + id})
	case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/keys/"):
		delete(f.liveKeys, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		w.WriteHeader(200)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
		f.deletedDevs = append(f.deletedDevs, strings.TrimPrefix(r.URL.Path, "/api/v2/device/"))
		w.WriteHeader(200)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
		w.Header().Set("ETag", `"p1"`)
		w.Write([]byte(fakePolicy))
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
		json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"message":"fakeTS: %s %s not implemented"}`, r.Method, r.URL.Path)
	}
}

// ---- fake setec (shared custody) ----

// curSetec is the running test's fake setec (tests may delete entries).
var curSetec *mgmtpg.FakeSetec

func setecDeletePrefix(prefix string) { curSetec.DeletePrefix(prefix) }

func setecDelete(name string) { curSetec.Delete(name) }

func newFakeSetec(t *testing.T) *mgmtpg.FakeSetec {
	curSetec = mgmtpg.NewFakeSetec(t)
	return curSetec
}

// ---- tw-mgmt replicas ----

// warmArgs sets every replica's warm-key pool cap from TW_MM_WARM_KEYS
// ("0" turns it off; unset keeps tw-mgmt's default, 5).
func warmArgs() []string {
	if n := os.Getenv("TW_MM_WARM_KEYS"); n != "" {
		return []string{"-warm-keys", n}
	}
	return nil
}

var buildOnce struct {
	sync.Once
	bin string
	err error
}

func mgmtBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tw-mm-bin-")
		if err != nil {
			buildOnce.err = err
			return
		}
		buildOnce.bin = filepath.Join(dir, "tw-mgmt")
		out, err := exec.Command("go", "build", "-o", buildOnce.bin, "../../cmd/tw-mgmt").CombinedOutput()
		if err != nil {
			buildOnce.err = fmt.Errorf("%v: %s", err, out)
		}
	})
	if buildOnce.err != nil {
		t.Fatal(buildOnce.err)
	}
	return buildOnce.bin
}

type cluster struct {
	t         *testing.T
	r         *run
	dsn       string
	ts        *fakeTS
	setec     *mgmtpg.FakeSetec
	tokenFile string
	token     string
	replicas  []*replica
	extraArgs []string
	repMu     sync.Mutex       // guards replica process state (chaos runs start/stop concurrently with calls)
	jsonc     string           // tailworks.jsonc for setupTenant ("" = twJSONC)
	env       map[int][]string // extra environment per replica
	argsFor   map[int][]string // extra flags per replica (after extraArgs)
	clients   map[int]*http.Client
}

// proxy returns replica i's Postgres fault proxy.
func (c *cluster) proxy(i int) *pgProxy { return c.replicas[i].pg }

type replica struct {
	pg     *pgProxy // this replica's path to Postgres (fault injection)
	i      int
	dir    string
	sock   string
	cmd    *exec.Cmd
	log    *os.File
	exited chan struct{}
}

// newCluster prepares n replicas (not started). Every replica shares the DSN,
// the fake TS API, the fake setec and one host token (pre-created, so
// concurrent starts don't race on creating it).
func newCluster(t *testing.T, r *run, n int) *cluster {
	t.Helper()
	mgmtBinary(t) // build first, so "start all at once" really is simultaneous
	c := &cluster{t: t, r: r, dsn: startPG(t, r), ts: newFakeTS(t), setec: newFakeSetec(t)}
	c.tokenFile = filepath.Join(r.dir, "host.token")
	c.token = strings.Repeat("ab", 32)
	if err := os.WriteFile(c.tokenFile, []byte(c.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		// unix socket paths are limited to ~104 bytes on macOS: keep them short.
		sock := filepath.Join(os.TempDir(), fmt.Sprintf("tw-mm-%d-%d.sock", os.Getpid(), i))
		c.replicas = append(c.replicas, &replica{i: i, dir: filepath.Join(r.dir, fmt.Sprintf("r%d", i)), sock: sock,
			pg: newPGProxy(t, mgmtpg.Host(t, c.dsn))})
	}
	t.Cleanup(func() {
		for _, rp := range c.replicas {
			c.stop(rp.i, syscall.SIGKILL)
		}
	})
	return c
}

func (c *cluster) start(i int) {
	c.t.Helper()
	rp := c.replicas[i]
	if rp.cmd != nil {
		c.t.Fatalf("replica %d already running", i)
	}
	os.MkdirAll(rp.dir, 0o700)
	lf, err := os.OpenFile(filepath.Join(c.r.dir, fmt.Sprintf("r%d.log", i)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		c.t.Fatal(err)
	}
	fmt.Fprintf(lf, "=== start %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	args := append([]string{"-local-dev", "-socket", rp.sock, "-host-token-file", c.tokenFile, "-state-dir", rp.dir,
		"-db", viaProxy(c.t, c.dsn, rp.pg.addr()), "-custody", "setec", "-setec-url", c.setec.URL, "-tsapi-url", c.ts.srv.URL, "-reap-interval", "2s",
		"-no-loopback"}, append(warmArgs(), c.extraArgs...)...)
	args = append(args, c.argsFor[i]...)
	cmd := exec.Command(mgmtBinary(c.t), args...)
	cmd.Env = append(os.Environ(), c.env[i]...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	c.r.record("tw-mgmt", fmt.Sprintf("r%d", i), cmd.Process.Pid)
	c.repMu.Lock()
	rp.cmd, rp.log, rp.exited = cmd, lf, make(chan struct{})
	ch := rp.exited
	c.repMu.Unlock()
	go func(cmd *exec.Cmd, ch chan struct{}) { cmd.Wait(); close(ch) }(cmd, ch)
}

// stop signals replica i (only the process this harness started) and waits
// up to 10 s for it to exit (SIGKILL after that).
func (c *cluster) stop(i int, sig syscall.Signal) {
	rp := c.replicas[i]
	c.repMu.Lock()
	if rp.cmd == nil {
		c.repMu.Unlock()
		return
	}
	c.repMu.Unlock()
	rp.cmd.Process.Signal(sig)
	select {
	case <-rp.exited:
	case <-time.After(10 * time.Second):
		rp.cmd.Process.Kill()
		<-rp.exited
	}
	fmt.Fprintf(rp.log, "=== stopped (%v) %s\n", sig, time.Now().UTC().Format(time.RFC3339Nano))
	rp.log.Close()
	c.repMu.Lock()
	rp.cmd = nil
	if hc := c.clients[i]; hc != nil {
		hc.CloseIdleConnections()
	}
	c.repMu.Unlock()
	os.Remove(rp.sock)
}

func (c *cluster) running(i int) bool {
	rp := c.replicas[i]
	c.repMu.Lock()
	defer c.repMu.Unlock()
	if rp.cmd == nil {
		return false
	}
	select {
	case <-rp.exited:
		return false
	default:
		return true
	}
}

// client returns replica i's HTTP client. One client (and transport) per
// replica socket, reused for every call: a new transport per call leaked an
// idle keep-alive connection per request, which over a 10-minute soak
// (~250k requests) exhausted the test process's threads and loaded the
// replicas with dead connections.
func (c *cluster) client(i int) *http.Client {
	c.repMu.Lock()
	defer c.repMu.Unlock()
	if c.clients == nil {
		c.clients = map[int]*http.Client{}
	}
	if hc := c.clients[i]; hc != nil {
		return hc
	}
	sock := c.replicas[i].sock
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
	c.clients[i] = hc
	return hc
}

// call does one API request on replica i as the local principal.
func (c *cluster) call(i int, method, path string, body any) (int, []byte, error) {
	return c.callAs(i, "", method, path, body)
}

// callAs does one API request on replica i, as host (Tw-Host-ID) when host
// is not empty, and returns status and body.
func (c *cluster) callAs(i int, host, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://tw-mgmt"+path, rd)
	req.Header.Set("Tw-Host-Token", c.token)
	if host != "" {
		req.Header.Set("Tw-Host-ID", host)
	}
	if body != nil {
		ct := "application/json"
		if method == "PATCH" {
			ct = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.client(i).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// waitReady waits until replica i answers GET /v1/status with 200, or the
// timeout passes. It returns how long that took.
func (c *cluster) waitReady(i int, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	var last string
	for time.Since(start) < timeout {
		if !c.running(i) {
			return time.Since(start), fmt.Errorf("replica %d exited (see r%d.log)", i, i)
		}
		st, b, err := c.call(i, "GET", "/v1/status", nil)
		if err == nil && st == 200 {
			return time.Since(start), nil
		}
		last = fmt.Sprintf("status=%d err=%v body=%.200s", st, err, b)
		time.Sleep(100 * time.Millisecond)
	}
	return time.Since(start), fmt.Errorf("replica %d not ready after %v: %s", i, timeout, last)
}

// db opens a direct connection to the cluster's Postgres (for the oracle).
func (c *cluster) db() *sql.DB {
	db, err := sql.Open("pgx", c.dsn)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { db.Close() })
	return db
}

// advisoryHolders returns the PIDs and application names of sessions that
// hold the migration advisory lock 7464.
func advisoryHolders(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT l.pid, COALESCE(a.state,''), COALESCE(a.application_name,''), l.granted
		FROM pg_locks l LEFT JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND l.objid = 7464`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pid int
		var state, app string
		var granted bool
		rows.Scan(&pid, &state, &app, &granted)
		out = append(out, fmt.Sprintf("pid=%d state=%s app=%q granted=%v", pid, state, app, granted))
	}
	return out, rows.Err()
}

// ---- tenant / host / deployment setup ----

const twJSONC = `{ "tailnet": { "objects": {
    "SupportAgent": { "tags": ["tag:tw-agent-support"], "agent": true },
    "LookupAgent":  { "tags": ["tag:tw-agent-support-sub"], "agent": true },
  },
  "spawn": { "maxLive": 64, "maxDepth": 3 } } }`

func makeBundle(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	return buf.Bytes()
}

// must does callAs on replica i and fails the test unless the status is 2xx;
// it decodes the body into out when out is not nil.
func (c *cluster) must(i int, host, method, path string, body, out any) {
	c.t.Helper()
	st, b, err := c.callAs(i, host, method, path, body)
	if err != nil || st/100 != 2 {
		c.t.Fatalf("r%d %s %s: %d %v %.300s", i, method, path, st, err, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("r%d %s %s: decode: %v", i, method, path, err)
		}
	}
}

// firstRunning returns the index of a running replica, or -1.
func (c *cluster) firstRunning() int {
	for i := range c.replicas {
		if c.running(i) {
			return i
		}
	}
	return -1
}

type setup struct {
	tenantID string
	workload string
	epoch    int64
}

// setupTenant creates a tenant (OAuth mode against the fake TS API), host
// h1, and a deployment of workload w, all through replica i.
func (c *cluster) setupTenant(i int, rpm int) setup {
	c.t.Helper()
	keyFile := filepath.Join(c.r.dir, "oauth.env")
	writeAPIKey(c.t, keyFile)
	var tn struct{ TenantID string }
	c.must(i, "", "POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": keyFile,
		"registrationsPerMinute": rpm}, &tn)
	c.must(i, "h1", "PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 10000, "tiers": []string{"process"}}, nil)
	var dep struct {
		Epoch int64
	}
	jsonc := twJSONC
	if c.jsonc != "" {
		jsonc = c.jsonc
	}
	bundle := makeBundle(map[string]string{"tailworks.jsonc": jsonc, "dist/index.js": "export default {}"})
	c.must(i, "", "POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "w", "bundle": bundle}, &dep)
	return setup{tenantID: tn.TenantID, workload: "w", epoch: dep.Epoch}
}

// heartbeat keeps host h1 live (and renews its lease) every 2 s through any
// running replica, until stop is closed.
func (c *cluster) heartbeat(s setup, stop <-chan struct{}) {
	go func() {
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			if i := c.firstRunning(); i >= 0 {
				c.callAs(i, "h1", "PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 0,
					"leases": []map[string]any{{"namespace": s.workload, "epoch": s.epoch}}})
			}
			select {
			case <-stop:
				return
			case <-tk.C:
			}
		}
	}()
}

// objectStatuses reads object ID → status straight from Postgres.
func objectStatuses(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT id, status FROM objects")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		rows.Scan(&id, &st)
		out[id] = st
	}
	return out, rows.Err()
}

// writeAPIKey writes the tenant's credential: an OAuth client file the
// fake TS API exchanges for fakeAPIToken (tailworks mints only with OAuth
// clients).
func writeAPIKey(t testing.TB, path string) { cliflow.WriteFakeOAuthFile(t, path) }
