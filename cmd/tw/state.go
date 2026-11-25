package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rosskukulinski/tailworks/mgmt"
	"golang.org/x/net/proxy"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// Config is <state>/tw.json, written by `tw up`.
type Config struct {
	// V is the tw.json format version (ADR-041 §4): 0 =
	// written before versioning (the same layout as 1).
	V        int    `json:"v"`
	Tailnet  string `json:"tailnet"`
	TenantID string `json:"tenantID,omitempty"`
	// MintMode is "oauth" (a Tailscale OAuth client whose secret stays in
	// a 0600 file) or "headscale" (--control=headscale: APIKeyFile holds a
	// Headscale API key; ADR-015). State written by an older tailworks may say
	// "apikey", "wif" or nothing (apikey, before M10): those modes were
	// removed (removedMode), and tw up --oauth-client-file switches such a
	// state to oauth.
	MintMode   string `json:"mintMode,omitempty"`
	APIKeyFile string `json:"apiKeyFile,omitempty"` // headscale: path only; the key itself is never copied
	// oauth mode: the client file's absolute path (read at use by the CLI
	// and tw-mgmt; never copied) and the client's public ID.
	OAuthClientFile string `json:"oauthClientFile,omitempty"`
	OAuthClientID   string `json:"oauthClientID,omitempty"`
	// mgmt's own tailnet node (tag:tw-mgmt), joined by `tw up` with a key
	// the tenant's credential mints; the API is served on it (WhoIs auth,
	// never Funnel).
	MgmtHostname string `json:"mgmtHostname,omitempty"` // tsnet hostname (default tw-mgmt)
	MgmtDNSName  string `json:"mgmtDNSName,omitempty"`  // tw-mgmt.<tailnet>.ts.net
	MgmtNodeID   string `json:"mgmtNodeID,omitempty"`   // stable node ID
	MgmtIP       string `json:"mgmtIP,omitempty"`       // tailnet IPv4
	HTTPS        bool   `json:"https,omitempty"`        // the tailnet has HTTPS certificates (tw link uses :443 then)
	ControlURL   string `json:"controlURL"`
	TSAPIURL     string `json:"tsapiURL,omitempty"`
	// LocalDev (tw up --local-dev): mgmt also serves a unix socket with
	// the per-host token and hostd talks to it there (hermetic tests, tailworks
	// dev). Normal operation is tsnet only: the CLI reaches mgmt through
	// mgmt's node loopback proxy, hostd through its own tag:tw-host node.
	LocalDev bool `json:"localDev,omitempty"`
	// Mode is the process layout `tw up` runs: "single" (the default for
	// new state since M13: one process, mgmt's node is the only
	// infrastructure device and the host agent runs embedded in it, pushes
	// and API calls in-process) or "separate" (tw up --separate-host, and
	// --local-dev: tw-mgmt and tw-hostd as two child processes, the host
	// on its own tag:tw-host node). Empty means separate: state written
	// before Mode existed keeps its shape.
	Mode string `json:"mode,omitempty"`
	// The store and custody mgmt runs with (tw up --db / --db-file,
	// --custody setec --setec-url; up_store.go). Empty: SQLite in
	// <state>/mgmt/mgmt.db and file custody in <state>/mgmt/custody.
	DB       string `json:"db,omitempty"`
	DBFile   string `json:"dbFile,omitempty"`
	Custody  string `json:"custody,omitempty"`
	SetecURL string `json:"setecURL,omitempty"`
	// PushPort is the port on hostd's node where mgmt pushes kills.
	PushPort       int    `json:"pushPort,omitempty"`
	HostID         string `json:"hostID"`
	HostnamePrefix string `json:"hostnamePrefix,omitempty"`
	Ephemeral      bool   `json:"ephemeral,omitempty"`
	// InsecureSharedHost (tw up --insecure-shared-host): without a process
	// sandbox, still serve process-tier workloads of several tenants
	// (incl. Node-tier apps whose egress is then unenforced, RT-08).
	InsecureSharedHost bool `json:"insecureSharedHost,omitempty"`
	// AllowExperimentalFlags (tw up --allow-experimental-flags): the host
	// lets deployments set workerd's experimental compatibility flags
	// (hostd/gen/compatflags.go, #337).
	AllowExperimentalFlags bool `json:"allowExperimentalFlags,omitempty"`
	// AllowSiblingEgress: objects dial other objects on this host over
	// WireGuard instead of netd's in-process hop (tests).
	AllowSiblingEgress bool `json:"allowSiblingEgress,omitempty"`
	// Fast identity (STATUS.md "Fast identity"). WarmKeys and PoolNodes are
	// pointers so state written before they existed (nil) keeps the
	// binaries' defaults instead of reading as 0 ("off").
	WarmKeys          *int   `json:"warmKeys,omitempty"`    // tw-mgmt -warm-keys
	PoolNodes         *int   `json:"poolNodes,omitempty"`   // tw-hostd -pool-nodes
	PortMapping       bool   `json:"portMapping,omitempty"` // tw-hostd -port-mapping
	Workerd           string `json:"workerd"`
	Heartbeat         string `json:"heartbeat"`
	KillTombstone     string `json:"killTombstone"`
	DeviceDeleteDelay string `json:"deviceDeleteDelay"`
	CreatedAt         string `json:"createdAt"`

	// Retention (up_retention.go; empty or 0 = off, the default).
	AuditMaxAge      string `json:"auditMaxAge,omitempty"`
	AuditMaxRecords  int64  `json:"auditMaxRecords,omitempty"`
	RequestLogMaxAge string `json:"requestLogMaxAge,omitempty"`

	// WorkerInternet (up_workerinternet.go): "direct" or "off"
	// (tw-hostd -worker-internet); empty = direct.
	WorkerInternet string `json:"workerInternet,omitempty"`

	// InstallPrefix is the install.sh prefix of the tw that last ran
	// `tw up` here (upgrade_prefix.go; issue #532), rewritten by every
	// tw up: empty for npm, dev builds and checkouts. tw upgrade trusts it
	// only while it is still an install prefix.
	InstallPrefix string `json:"installPrefix,omitempty"`

	// Tenants are the tenants added with `tw tenant add` (the operator
	// tenant above is the default for every command).
	Tenants []TenantRef `json:"tenants,omitempty"`
}

type state struct {
	Dir string
	Cfg Config
	// Link is <state>/link.json (`tw link`): this machine drives a remote
	// mgmt over the tailnet instead of a local `tw up` stack.
	Link *Link

	remoteTenants []TenantRef
}

// Process layouts (Config.Mode).
const (
	ModeSingle   = "single"
	ModeSeparate = "separate"
)

// single reports whether the host agent runs embedded in mgmt's process.
func (c Config) single() bool { return c.Mode == ModeSingle }

// modeLabel names the layout for humans.
func (c Config) modeLabel() string {
	if c.single() {
		return ModeSingle
	}
	return ModeSeparate
}

// headscale reports whether this state runs the Headscale profile (ADR-015).
func (c Config) headscale() bool { return c.MintMode == "headscale" }

// oauth reports whether this state mints through an OAuth client (M12).
func (c Config) oauth() bool { return c.MintMode == "oauth" }

// modeName is the mint mode's short name ("apikey" for pre-M10 state).
func (c Config) modeName() string {
	if c.MintMode == "" {
		return "apikey"
	}
	return c.MintMode
}

// mintMode names the mode for humans.
func (c Config) mintMode() string { return c.modeName() }

// removedMode reports whether the state is in a mint mode tailworks no longer
// supports (apikey, wif; ADR-042).
func (c Config) removedMode() bool { return !c.oauth() && !c.headscale() }

// errRemovedMode is the refusal for a state dir in a removed mint mode:
// tw up --oauth-client-file switches it (tenant, tailnet and deployments
// are kept).
func (c Config) errRemovedMode(dir string) error {
	return fail("mint_mode_removed", dir+" is in mint mode "+c.modeName()+", which tailworks no longer supports",
		"Tailscale tailnets now mint with an OAuth client only; switching keeps the tenant, its deployments and mgmt's node").
		withFix("create an OAuth client (admin console › Settings › Trust credentials: Auth Keys write, tag tag:tw; see docs/INSTALL.md), save it 0600, then:",
			"tw up --oauth-client-file <file>")
}

func stateDirFrom(flagVal string) (string, error) {
	d := flagVal
	if d == "" {
		d = os.Getenv("TW_STATE_DIR")
	}
	if d == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(home, ".tailworks")
	}
	return filepath.Abs(d)
}

func openState(flagVal string) (*state, error) {
	d, err := stateDirFrom(flagVal)
	if err != nil {
		return nil, err
	}
	s := &state{Dir: d}
	if l, err := loadLink(s.linkPath()); err == nil {
		s.Link = l
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, decodeConfig(b, &s.Cfg)
}

// configVersion is the tw.json format version this tailworks writes.
const configVersion = 1

// decodeConfig parses tw.json, refusing a version newer than this tailworks
// knows (a newer tailworks wrote it).
func decodeConfig(b []byte, c *Config) error {
	if err := json.Unmarshal(b, c); err != nil {
		return err
	}
	if c.V > configVersion {
		return fmt.Errorf("tw.json format v%d was written by a newer tailworks (this one reads up to v%d); upgrade tw", c.V, configVersion)
	}
	return nil
}

func (s *state) configured() bool { return s.Cfg.Tailnet != "" }

// remote reports whether commands go to a linked mgmt over the tailnet:
// link.json exists and this machine has no local `tw up` state.
func (s *state) remote() bool { return s.Link != nil && !s.configured() }

func (s *state) requireUp() error {
	if s.remote() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := s.whoami(ctx); err != nil {
			return err
		}
		return nil
	}
	if !s.configured() {
		return fail("not_up", "tw is not set up in "+s.Dir).withFix("tw up --oauth-client-file <file>   (on the host)", "tw link <tw-mgmt.<tailnet>.ts.net>   (on a developer machine)")
	}
	if !s.supervisorAlive() {
		return fail("not_running", "tw is set up in "+s.Dir+" but not running").withFix("tw up")
	}
	return nil
}

func (s *state) configPath() string { return filepath.Join(s.Dir, "tw.json") }
func (s *state) linkPath() string   { return filepath.Join(s.Dir, "link.json") }
func (s *state) mgmtTsnetDir() string {
	return filepath.Join(s.Dir, "mgmt", "tsnet") // tw-mgmt -state-dir <state>/mgmt, tsnet under it
}

func (s *state) save() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	s.Cfg.V = configVersion
	b, _ := json.MarshalIndent(s.Cfg, "", "  ")
	return os.WriteFile(s.configPath(), b, 0o600)
}

// runDir holds unix sockets. macOS limits socket paths to 104 bytes, and
// hostd nests instance sockets two levels below, so long state dirs (test
// temp dirs) get a short, deterministic /tmp dir instead.
func (s *state) runDir() string {
	d := filepath.Join(s.Dir, "run")
	if len(d) <= 48 {
		return d
	}
	h := sha256.Sum256([]byte(s.Dir))
	return filepath.Join("/tmp", "tw-"+hex.EncodeToString(h[:4]))
}

func (s *state) mgmtSock() string       { return filepath.Join(s.runDir(), "mgmt.sock") }
func (s *state) hostTokenFile() string  { return filepath.Join(s.Dir, "hostd", "mgmt.token") }
func (s *state) hostdRunDir() string    { return filepath.Join(s.runDir(), "h") }
func (s *state) hostdSock() string      { return filepath.Join(s.hostdRunDir(), "hostd.sock") }
func (s *state) logDir() string         { return filepath.Join(s.Dir, "logs") }
func (s *state) pidFile() string        { return filepath.Join(s.Dir, "supervisor.pid") }
func (s *state) log(name string) string { return filepath.Join(s.logDir(), name+".log") }

func (s *state) supervisorPID() int {
	b, err := os.ReadFile(s.pidFile())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	// After a reboot (or a power cut) the file is stale and its pid may
	// belong to another process, even another tailworks: a supervisor wrote the
	// file after it started, so a process that started later isn't it.
	if st, err := os.Stat(s.pidFile()); err == nil {
		if start, ok := procStartTime(pid); ok && start.After(st.ModTime().Add(2*time.Second)) {
			return 0
		}
	}
	return pid
}

// procStartTime is when pid started (Linux /proc; ok=false elsewhere).
func procStartTime(pid int) (time.Time, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, false
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return time.Time{}, false
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64) // field 22: starttime, clock ticks after boot
	if err != nil {
		return time.Time{}, false
	}
	sb, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, ln := range strings.Split(string(sb), "\n") {
		if v, ok := strings.CutPrefix(ln, "btime "); ok {
			bt, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Unix(bt, 0).Add(time.Duration(ticks) * time.Second / 100), true // USER_HZ = 100
		}
	}
	return time.Time{}, false
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// After a reboot the pid file's number may belong to another process:
	// on Linux, only a tailworks binary counts.
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		argv0, _, _ := strings.Cut(string(b), "\x00")
		b := filepath.Base(argv0)
		return strings.HasPrefix(b, "tw") || b == "tailworks" // the install.sh symlink
	}
	return true
}

func (s *state) supervisorAlive() bool { return pidAlive(s.supervisorPID()) }

// mgmt is the CLI's client for mgmt. Locally (`tw up` on this machine)
// it is the unix socket with the per-host token hostd uses (RT-03):
// same-uid peercred alone admits nobody, and the token file is readable
// only by the tailworks user. Linked (`tw link`), it is HTTPS to mgmt's tailnet
// node over this machine's own Tailscale connection, authenticated by
// WhoIs: the caller's tailnet identity, no token.
func (s *state) loopbackPath() string { return filepath.Join(s.Dir, "mgmt", "loopback.json") }
func (s *state) hostJoinFile() string { return filepath.Join(s.Dir, "hostd", "join.env") }
func (s *state) hostTsnetDir() string { return filepath.Join(s.Dir, "hostd", "tsnet") }

// loopback reads mgmt's loopback descriptor (nil if mgmt is not up).
func (s *state) loopback() *mgmt.Loopback {
	b, err := os.ReadFile(s.loopbackPath())
	if err != nil {
		return nil
	}
	var lb mgmt.Loopback
	if json.Unmarshal(b, &lb) != nil || lb.Addr == "" || lb.MgmtIP == "" {
		return nil
	}
	return &lb
}

// transport names how this CLI reaches mgmt (tw doctor, tw status).
func (s *state) transport() string {
	switch {
	case s.remote() && s.Link.Tsnet != nil:
		return "tsnet (link, CLI node)"
	case s.remote():
		return "tsnet (link, this machine's Tailscale)"
	case s.Cfg.LocalDev:
		return "local-dev (unix socket + host token)"
	case s.Cfg.single():
		return "tsnet (loopback through mgmt's node; host embedded in the mgmt process)"
	}
	return "tsnet (loopback through mgmt's node)"
}

// mgmt is the client for this state: the linked endpoint, the local-dev
// unix socket, or (normal operation) mgmt's own tailnet IP dialled through
// the node's SOCKS5 loopback proxy, so the request carries mgmt's identity
// and WhoIs makes it the operator.
func (s *state) mgmt() *mgmtapi.Client {
	return withVersionCheck(s.mgmtRaw()) // versioncheck.go
}

func (s *state) mgmtRaw() *mgmtapi.Client {
	if s.remote() {
		return s.Link.client()
	}
	if s.Cfg.LocalDev {
		c, _ := mgmtapi.NewClient("unix://" + s.mgmtSock())
		c.TokenFile = s.hostTokenFile()
		return c
	}
	lb := s.loopback()
	if lb == nil {
		// mgmt not up yet (or not in this mode): a client that fails fast.
		c, _ := mgmtapi.NewClient("http://127.0.0.1:1")
		c.HTTP = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("tw-mgmt's loopback descriptor %s is not there (mgmt not running?)", s.loopbackPath())
		}}}
		return c
	}
	c, _ := mgmtapi.NewClient("http://" + lb.MgmtIP)
	c.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DialContext: socksDialer(lb.Addr, lb.ProxyCred), MaxIdleConnsPerHost: 8}}
	return c
}

// socksDialer dials through tsnet's loopback SOCKS5 proxy (user "tsnet").
func socksDialer(addr, cred string) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		d, err := proxy.SOCKS5("tcp", addr, &proxy.Auth{User: "tsnet", Password: cred}, &net.Dialer{Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
		if cd, ok := d.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, address)
		}
		return d.Dial(network, address)
	}
}

func (s *state) hostd() *mgmtapi.Client {
	c, _ := mgmtapi.NewClient("unix://" + s.hostdSock())
	return c
}

func (s *state) mgmtHealthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.mgmt().Do(ctx, "GET", "/v1/healthz", nil, nil) == nil
}

// ---- binaries ----

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}

// findBin finds a tailworks binary next to this executable, then on PATH.
func findBin(name string) (string, error) {
	if d := exeDir(); d != "" {
		if p := filepath.Join(d, name); isFile(p) {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fail("not_installed", name+" not found next to tailworks ("+exeDir()+") or on PATH").withFix("make build (in the tailworks repo), or reinstall tw")
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// repoRoots are directories that may hold a tailworks checkout or install
// (share/tailworks): above this executable and above the working directory.
func repoRoots() []string {
	var out []string
	seen := map[string]bool{}
	add := func(start string) {
		for d := start; d != "" && d != "/" && d != "."; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d, filepath.Join(d, "share", "tailworks"))
			}
		}
	}
	add(exeDir())
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	return out
}

func findWorkerd(flagVal string) (string, error) {
	cands := []string{flagVal, os.Getenv("TW_WORKERD")}
	if d := exeDir(); d != "" {
		cands = append(cands, filepath.Join(d, "workerd"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands, filepath.Join(home, ".tailworks", "bin", "workerd"))
	}
	for _, c := range cands {
		if c != "" && isFile(c) {
			return filepath.Abs(c)
		}
	}
	if p, err := exec.LookPath("workerd"); err == nil {
		return p, nil
	}
	for _, r := range repoRoots() {
		if p := filepath.Join(r, "node_modules", ".bin", "workerd"); isFile(p) {
			return p, nil
		}
	}
	return "", fail("not_installed", "workerd not found").withFix("pass --workerd <path> or set $TW_WORKERD (tw pins workerd 1.20261121.1)")
}

// createTailworksDir finds the create-tailworks package (bundler + templates).
func createTailworksDir() (string, error) {
	if d := os.Getenv("TW_CREATE_TAILWORKS"); d != "" {
		return d, nil
	}
	for _, r := range repoRoots() {
		for _, c := range []string{filepath.Join(r, "js", "packages", "create-tailworks"), filepath.Join(r, "create-tailworks"), filepath.Join(r, "node_modules", "create-tailworks")} {
			if isFile(filepath.Join(c, "lib", "build.mjs")) {
				return c, nil
			}
		}
	}
	return "", fail("not_installed", "create-tailworks (bundler and templates) not found").withFix("set $TW_CREATE_TAILWORKS to js/packages/create-tailworks of a tailworks checkout")
}

// nodePaths are module fallbacks for bundling apps outside the tailworks repo
// (@tailworks/tailnet, @tailworks/mcp, agents, … from tailworks' own node_modules).
func nodePaths(createTw string) []string {
	var out []string
	for d := createTw; d != "/" && d != "."; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, "node_modules")); err == nil && st.IsDir() {
			out = append(out, filepath.Join(d, "node_modules"))
		}
	}
	return out
}

// identityMgmtArgs are tw-mgmt's fast-identity flags from c (none for
// state written before they existed: the binary's defaults apply).
func (c Config) identityMgmtArgs() []string {
	if c.WarmKeys == nil {
		return nil
	}
	return []string{"-warm-keys", strconv.Itoa(*c.WarmKeys)}
}

// identityHostdArgs are tw-hostd's fast-identity flags from c.
func (c Config) identityHostdArgs() []string {
	var a []string
	if c.PoolNodes != nil {
		a = append(a, "-pool-nodes", strconv.Itoa(*c.PoolNodes))
	}
	if c.PortMapping {
		a = append(a, "-port-mapping")
	}
	return a
}
