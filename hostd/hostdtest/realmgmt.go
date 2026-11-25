package hostdtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/inproc"
	"github.com/rosskukulinski/tailworks/mgmt"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Backends: the mgmt an Env's hostd talks to (Options.Backend).
const (
	// BackendFake is hostd/fakemgmt on a unix socket (the default).
	BackendFake = "fakemgmt"
	// BackendReal is the real mgmt (package mgmt: SQLite store, file
	// custody, a fake Tailscale API whose keys testcontrol accepts), in
	// this process. hostd reaches it through internal/inproc, as the host
	// embedded in single-process tw up does, and mgmt pushes through the
	// host's PushHandler; the CLI side uses its unix socket (LocalHandler).
	BackendReal = "real"
)

// Backends lists both, for tests that run one body against each.
var Backends = []string{BackendFake, BackendReal}

// Hooks are the test controls a backend offers besides /v1: counters of
// what hostd did and a partition switch. fakemgmt implements them itself;
// for the real mgmt they are a middleware on the host's side of the
// transport (realHooks), so they count exactly what crossed the wire.
type Hooks interface {
	// StoreAppends counts accepted journal appends (POST …/journal, 201).
	StoreAppends() int
	// SecretGrants counts secret grants handed out (POST …/secret-grants, 201).
	SecretGrants() int
	// SetHeartbeatsDown answers the host's heartbeats 503 store_unavailable
	// (mgmt partitioned from this host) while down.
	SetHeartbeatsDown(down bool)
	// DelayHeartbeat answers the host's next heartbeat only after d (a slow
	// mgmt, or a loaded host: the heartbeat succeeds, late).
	DelayHeartbeat(d time.Duration)
	// QueueClaims counts messages the host claimed from the queue.
	QueueClaims(tenantID, queue string) int
	// QueueBacklog is the queue's backlog (-1: no such queue).
	QueueBacklog(tenantID, queue string) int
	// SetReadyFault delays (and optionally fails) the host's ready reports
	// until reset with the zero value (issue #331).
	SetReadyFault(f fakemgmt.ReadyFault)
}

var _ Hooks = (*fakemgmt.Server)(nil)

// realBackend is a running real mgmt.
type realBackend struct {
	M     *mgmt.Mgmt
	cli   *mgmtapi.Client // admin (local principal) over the unix socket
	host  *mgmtapi.Client // host h1 over internal/inproc
	hooks *realHooks
}

// startRealMgmt starts mgmt in dir and returns its clients. Every /v1
// exchange, on both sides, is checked against api/openapi.yaml.
func startRealMgmt(t testing.TB, dir, hostID string, log *slog.Logger, clock mgmt.Clock) *realBackend {
	t.Helper()
	ctx := context.Background()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if clock != nil {
		st.SetClock(clock) // the store's clock is the one leases expire by
	}
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	ts := fakeTailscaleAPI(t)
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, Broker: &mgmt.Broker{BaseURL: ts.URL},
		IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Second, PolicyMaxAge: time.Minute, Log: log,
		Clock: clock})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		st.Close()
		t.Fatal(err)
	}
	// Closed after hostd (cleanups run last-registered first).
	t.Cleanup(func() { m.Close(); st.Close() })
	val, err := v1.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	h := val.Middleware(func(err error) { t.Errorf("mgmt /v1 spec violation: %v", err) })((&mgmt.Server{M: m}).Handler())

	sock := filepath.Join(dir, "mgmt.sock")
	ln, err := mgmt.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	token := mgmt.NewHostToken()
	srv := &http.Server{Handler: mgmt.LocalHandler(h, token)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	cli, _ := mgmtapi.NewClient("unix://" + sock)
	cli.Token = token

	hooks := &realHooks{cli: cli, claims: map[string]int{}}
	return &realBackend{M: m, cli: cli, host: inproc.NewClient(hooks.wrap(h), hostID), hooks: hooks}
}

// realTenant adapts a fakemgmt-style tenant (mint mode "static") to real
// mgmt: an OAuth client the fake Tailscale API knows, written to dir.
func realTenant(t testing.TB, dir string, tn mgmtapi.Tenant) mgmtapi.Tenant {
	t.Helper()
	if tn.MintMode == "" || tn.MintMode == "static" {
		f := filepath.Join(dir, "oauth.env")
		if _, err := os.Stat(f); err != nil {
			if err := os.WriteFile(f, []byte("TS_OAUTH_CLIENT_ID="+fakeOAuthID+"\nTS_OAUTH_CLIENT_SECRET="+fakeOAuthSecret+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		tn.MintMode, tn.OAuthClientFile = "oauth", f
	}
	return tn
}

// The fake Tailscale API's OAuth client (built at runtime: no key-shaped
// literal in the repo).
var (
	fakeOAuthID     = "kHostdTestCNTRL"
	fakeOAuthSecret = "tskey-" + "client-" + fakeOAuthID + "-" + strings.Repeat("S", 24)
)

// fakeTailscaleAPI is the part of the Tailscale API mgmt uses here: the
// OAuth exchange, auth keys (testcontrol accepts any string), deletes,
// the policy and the device list.
func fakeTailscaleAPI(t testing.TB) *httptest.Server {
	const token = "fake-api-token"
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != token {
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
			// The grant is the one the hostd tests' testcontrol hands every
			// node (startSuite), so spawns may delegate it (CheckSpawnCaps).
			w.Write([]byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]},
				"grants": [{"src": ["tag:ttest"], "dst": ["tag:ttest"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}]}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
			json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// realHooks implements Hooks for the real mgmt as a middleware on the
// host's transport.
type realHooks struct {
	cli *mgmtapi.Client

	mu      sync.Mutex
	appends int
	grants  int
	down    bool
	hbDelay time.Duration  // the next heartbeat's delay (DelayHeartbeat)
	claims  map[string]int // tenantID|queue → messages claimed

	readyFault fakemgmt.ReadyFault // SetReadyFault
}

func (k *realHooks) SetReadyFault(f fakemgmt.ReadyFault) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.readyFault = f
}

func (k *realHooks) StoreAppends() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.appends
}

func (k *realHooks) SecretGrants() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.grants
}

func (k *realHooks) SetHeartbeatsDown(down bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.down = down
}

func (k *realHooks) DelayHeartbeat(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.hbDelay = d
}

func (k *realHooks) QueueClaims(tenantID, queue string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.claims[tenantID+"|"+queue]
}

func (k *realHooks) QueueBacklog(tenantID, queue string) int {
	var q struct {
		Backlog int `json:"backlog"`
	}
	if err := k.cli.Do(context.Background(), "GET", "/v1/tenants/"+tenantID+"/queues/"+queue, nil, &q); err != nil {
		return -1
	}
	return q.Backlog
}

// wrap counts and partitions the host's requests to h.
func (k *realHooks) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		last := p[len(p)-1]
		switch {
		case r.Method == "PUT" && len(p) == 4 && p[1] == "hosts" && last == "status":
			k.mu.Lock()
			down, delay := k.down, k.hbDelay
			k.hbDelay = 0
			k.mu.Unlock()
			if delay > 0 {
				time.Sleep(delay)
			}
			if down {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Tw-Error", "store_unavailable")
				w.WriteHeader(503)
				w.Write([]byte(`{"code":"store_unavailable","message":"mgmt is partitioned from this host (test)"}`))
				return
			}
		case r.Method == "PATCH" && len(p) == 3 && p[1] == "objects": // a ready report
			k.mu.Lock()
			f := k.readyFault
			k.mu.Unlock()
			if f.Apply(w) {
				return
			}
		case r.Method == "POST" && last == "journal", r.Method == "POST" && last == "secret-grants":
			rec := &statusRecorder{ResponseWriter: w}
			h.ServeHTTP(rec, r)
			if rec.status == 201 {
				k.mu.Lock()
				if last == "journal" {
					k.appends++
				} else {
					k.grants++
				}
				k.mu.Unlock()
			}
			return
		case r.Method == "POST" && len(p) == 7 && p[3] == "queues" && last == "pull":
			rec := &statusRecorder{ResponseWriter: w, keep: true}
			h.ServeHTTP(rec, r)
			var out struct {
				Messages []json.RawMessage `json:"messages"`
			}
			if rec.status == 200 && json.Unmarshal(rec.body.Bytes(), &out) == nil && len(out.Messages) > 0 {
				k.mu.Lock()
				k.claims[p[2]+"|"+p[4]] += len(out.Messages)
				k.mu.Unlock()
			}
			return
		}
		h.ServeHTTP(w, r)
	})
}

// statusRecorder records the status (and with keep, the body) it passes on.
type statusRecorder struct {
	http.ResponseWriter
	status int
	keep   bool
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	if r.keep {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

// Flush passes a streamed response on.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// startReal is Start with BackendReal.
func startReal(t testing.TB, o Options, dir, wd string, log *slog.Logger) *Env {
	t.Helper()
	if o.Fake.RegistryDir != "" || o.Images != nil {
		t.Fatal("hostdtest: BackendReal has no registry wiring (Fake.RegistryDir, Images)")
	}
	// mgmt's state is not under dir: tests walk hostd's directories (no
	// secret value outside an instance dir, say), and mgmt's are not hostd's.
	mdir, err := ShortDir("tw-mg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.Getenv("TW_KEEP_INSTANCES") == "" {
			os.RemoveAll(mdir)
		}
	})
	rb := startRealMgmt(t, mdir, "h1", log, o.MgmtClock)
	var tr struct {
		TenantID string `json:"tenantID"`
	}
	if err := rb.cli.Do(context.Background(), "POST", "/v1/tenants", tenantCreate(realTenant(t, mdir, o.Tenant)), &tr); err != nil {
		t.Fatal(err)
	}
	cfg := hostConfig(o, dir, wd, log, rb.host)
	cfg.PushInProcess = true
	e := &Env{T: t, Mgmt: rb.cli, TenantID: tr.TenantID, Dir: dir, cfg: cfg, Hooks: rb.hooks, RealMgmt: rb.M}
	e.Host = e.startHost(cfg)
	return e
}

// startHost starts a hostd with cfg; with the real mgmt its push handler
// is registered first (it registers with push URL inproc://).
func (e *Env) startHost(cfg hostd.Config) *hostd.Host {
	e.T.Helper()
	h, err := hostd.New(cfg)
	if err != nil {
		e.T.Fatal(err)
	}
	if e.RealMgmt != nil {
		e.RealMgmt.SetInProcessPush(cfg.HostID, h.PushHandler())
	}
	if err := h.Start(context.Background()); err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(func() { h.Close() })
	return h
}
