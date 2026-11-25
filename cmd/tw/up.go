package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/inproc"
	"github.com/rosskukulinski/tailworks/internal/mgmtd"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd"
)

const prodControl = "https://controlplane.tailscale.com"

// baseSnippet is the infrastructure part of the tenant policy:
// tag:tw owns tailworks' tags (DX.md quickstart), plus the tags of mgmt's and
// the hosts' own nodes and the grants between them (normal operation is
// tsnet only: hostd talks to mgmt over the tailnet, mgmt pushes kills to
// the host's node).
func baseSnippet() policy.Snippet { return policy.InfraSnippet(defaultPushPort) }

// defaultPushPort is where hostd's node accepts mgmt's pushes.
const defaultPushPort = 7474

// operatorSnippet is what `tw up` needs in the tailnet policy (oauth
// mode): InfraSnippet (tag:tw owns
// tag:tw-mgmt and tag:tw-host, so the OAuth client mints both node keys;
// host ⇄ mgmt grants) plus the grant that makes autogroup:admin members
// tailworks admins over the tailnet (tw link). No nodeAttrs: no Funnel.
func operatorSnippet() policy.Snippet {
	s := policy.InfraSnippet(defaultPushPort)
	s.Grants = append(s.Grants, policy.AdminGrant("autogroup:admin", mgmt.MgmtTag, mgmt.AdminCap))
	return s
}

func tenantIDFor(tailnet string) string {
	l, _, _ := strings.Cut(tailnet, ".")
	l = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(l), "-")
	return "tn_" + strings.Trim(l, "-")
}

func defaultHostID() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(strings.ToLower(h), ".")
	h = strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(h, "-"), "-")
	if h == "" {
		h = "local"
	}
	return "h-" + h
}

type upResult struct {
	StateDir   string `json:"stateDir"`
	Tailnet    string `json:"tailnet"`
	MintMode   string `json:"mintMode"`
	TenantID   string `json:"tenantID"`
	HostID     string `json:"hostID"`
	Started    bool   `json:"started"`
	MgmtNode   string `json:"mgmtNode,omitempty"` // tw-mgmt.<tailnet>.ts.net
	MgmtNodeID string `json:"mgmtNodeID,omitempty"`
	MgmtIP     string `json:"mgmtIP,omitempty"`
	HTTPS      bool   `json:"https,omitempty"`
	PolicyOK   bool   `json:"policyOK"`
	PolicyDiff string `json:"policySnippet,omitempty"`
	Applied    bool   `json:"policyApplied"`
	// Mode is the process layout: "single" (one process; the host agent
	// embedded in mgmt) or "separate" (tw-mgmt + tw-hostd children).
	Mode string `json:"mode"`
	// ScaleOut is the policy still missing for `tw host add` (tag:tw-host
	// ownership and the host ⇄ mgmt grants): advisory in single mode.
	ScaleOut   string             `json:"scaleOutSnippet,omitempty"`
	Service    string             `json:"service,omitempty"`
	LingerHint string             `json:"lingerHint,omitempty"`
	SleepHint  string             `json:"sleepHint,omitempty"`
	Timings    map[string]float64 `json:"timingsMs"`

	// UnsafeFlags: the saved unsafe tw up flags (up_unsafe.go).
	UnsafeFlags []string `json:"unsafeFlags"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

const defaultMgmtHostname = "tw-mgmt"

func cmdUp(args []string) error {
	f := newFlags("up")
	upFlagGroups(f) // up_help.go: --help's groups
	tailnet := f.String("tailnet", "", "the tailnet to serve (oauth: read from the client's devices or mgmt's node; headscale: required, the dns.base_domain). Overrides discovery")
	oauthFile := f.String("oauth-client-file", "", "mint mode \"oauth\" (Tailscale): file with TS_OAUTH_CLIENT_SECRET=tskey-client-… (an OAuth client with Auth Keys write, tag tag:tw; tightened to 0600). Default on the default state dir: ~/.config/tailworks/oauth.env, asked for and saved on the first tw up")
	keyFile := f.String("api-key-file", "", "Headscale only: 0600 file with HEADSCALE_API_KEY=… (read at use, never copied); with a non-default --control-url it selects the Headscale profile. Default on the default state dir with a Headscale --control-url: ~/.config/tailworks/headscale.env when it exists. Tailscale API keys are not a mint mode: use --oauth-client-file")
	yes := yesFlagBefore(f, "--apply-policy writes the policy")
	apply := f.Bool("apply-policy", false, "write the tagOwners and grants tailworks needs to the tailnet policy instead of printing them (additive, validated first, written with If-Match); tw up shows the lines and asks first (--yes to skip). Without the flag tw up asks only on a terminal (not with --json or --yes); --apply-policy=false never asks or writes. Tailscale: the OAuth client needs Policy File write. Headscale: needs policy.mode database")
	control := f.String("control-url", prodControl, "coordination server for the tailnet's nodes")
	profileF := f.String("control", controlTailscale, "control-plane profile: tailscale, or headscale (ADR-015: a Headscale server; implied by --api-key-file with a non-default --control-url: tw up --control-url <url> --tailnet <base_domain> [--api-key-file <file>]; the file holds HEADSCALE_API_KEY=…, --tailnet is its dns.base_domain; docs/headscale.md)")
	tsapiURL := f.String("tsapi-url", "", "Tailscale API base URL (testing)")
	hostID := f.String("host-id", defaultHostID(), "this host's ID")
	prefix := f.String("hostname-prefix", "tw-", "prefix for every object node hostname")
	ephemeral := f.Bool("ephemeral", false, "join object nodes as ephemeral; `tw down` then deletes every device (tests, demos)")
	insecureSH := f.Bool("insecure-shared-host", false, "on a host without a process sandbox (Linux without Landlock), still serve process-tier workloads of more than one tenant; tenants are then NOT isolated from each other")
	allowExp := f.Bool("allow-experimental-flags", false, "let deployments on this host set compatibility flags workerd marks experimental (unsafe_module, rtti_api, precise_timers, …); refused by default (docs/COMPAT.md#experimental-flags)")
	workerdFlag := f.String("workerd", "", "workerd binary (default: $TW_WORKERD, the install's, or PATH)")
	// up_tuning.go: --heartbeat, --kill-tombstone, --device-delete-delay, --push-port
	addRemovedUpFlags(f)
	retention := addRetentionFlags(f) // up_retention.go
	workerInet := addWorkerInternetFlag(f)
	siblings := f.Bool("allow-sibling-egress", false, "dial other objects on this host over WireGuard (a hairpin) instead of netd's in-process hop (tests)")
	warmKeys := f.Int("warm-keys", mgmtd.DefaultWarmKeys, "pre-minted single-use keys kept per (tenant, tag set), so a new object skips the key-API call; sized from the recent create rate, capped here (0 = off)")
	poolNodes := f.Int("pool-nodes", hostdrun.DefaultPoolNodes, "pre-joined nodes kept per hot class (a create here in the last hour), so a new object is renamed onto one (~0.2 s) instead of joining a new node; each is a device (0 = off)")
	portMapping := f.Bool("port-mapping", false, "use UPnP/NAT-PMP/PCP for this host's nodes (off by default: it adds ~250 ms to every new node's join and has every node ask the router for a mapping; turn on behind hard NATs that need router-assisted direct paths)")
	service := f.Bool("service", false, "run the supervisor as a service that starts at login: Linux systemd user unit (~/.config/systemd/user/tw.service), macOS LaunchAgent (~/Library/LaunchAgents/dev.tailworks.up.plist). Default: on for a new default state dir where available; an existing state dir keeps how it runs (--service=false runs it in the background without a service)")
	mgmtHost := f.String("mgmt-hostname", "", "mgmt's own tailnet node name (default "+defaultMgmtHostname+")")
	mgmtKey := f.String("mgmt-authkey-file", "", "TS_AUTHKEY=… file for mgmt's node (unattended join: tests, automation); default: the tenant's credential mints it")
	localDev := f.Bool("local-dev", false, "DEVELOPMENT ONLY: hostd and the CLI use mgmt's unix socket with the per-host token instead of the tailnet (hermetic tests). Normal operation is tsnet only")
	storeF := addStoreFlags(f) // up_store.go: --db, --db-file, --custody, --setec-url
	separate := f.Bool("separate-host", false, "run tw-hostd as its own process on its own tag:tw-host tailnet node (the scale-out layout; default: ONE process, the host agent embedded in tw-mgmt, mgmt's node the only infrastructure device). --local-dev implies it")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fail("bad_request", "tw up takes no arguments", "usage: tw "+usageOf("up"))
	}
	warnRemovedUpFlags(f.FlagSet, os.Stderr)
	pmode := policyModeOf(f, *apply, *yes) // policy_ask.go: ask on a terminal (#481)
	if _, err := storeF.validate(); err != nil {
		return err
	}
	t0 := time.Now()
	timings := map[string]float64{}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	// The mint mode: the flag, else the state dir's, else the choice is
	// printed. Tailscale tailnets mint with an OAuth client; the Headscale
	// profile with a Headscale API key (ADR-042).
	if headscaleInferred(f.FlagSet, *keyFile, *control) { // up_headscale.go
		*profileF = controlHeadscale
	}
	if *profileF == controlHeadscale && *oauthFile != "" {
		return fail("profile_unsupported", "the Headscale profile mints with a Headscale API key: OAuth clients are a Tailscale feature").
			withFix("tw up --control-url <server_url> --tailnet <base_domain> --api-key-file <file with HEADSCALE_API_KEY=…>")
	}
	if *profileF != controlHeadscale && *keyFile != "" {
		return fail("mint_mode_removed", "tw no longer mints with a Tailscale API key (--api-key-file is for --control=headscale only)",
			"an OAuth client is scoped to auth keys and tag:tw, and its secret never expires; an API key could do anything its owner can").
			withFix("create an OAuth client (admin console › Settings › Trust credentials: Auth Keys write, tag tag:tw; see docs/INSTALL.md), save it 0600, then:",
				"tw up --oauth-client-file <file>", headscaleOnHint)
	}
	mode := ""
	switch {
	case *oauthFile != "":
		mode = "oauth"
	case *keyFile != "":
		mode = mgmt.MintHeadscale
	}
	if s.configured() {
		keepSavedUpFlags(f.FlagSet, s.Cfg) // up_saved.go
		was := s.Cfg.modeName()
		switch {
		case s.Cfg.removedMode() && mode == "oauth":
			// An older tailworks' apikey or wif state switches to oauth: the
			// tenant is PATCHed below; tailnet, deployments and mgmt's
			// node are kept.
			fmt.Fprintf(os.Stderr, "› mint     switching %s from the removed mint mode %s to oauth (OAuth client)\n", s.Dir, was)
			if was == "wif" {
				fmt.Fprintf(os.Stderr, "› wif      the WIF issuer node (tag:tw-issuer) and its trust credentials are no longer used: remove them in the admin console, and tag:tw-issuer and its funnel nodeAttrs from your policy\n")
			}
			s.Cfg.APIKeyFile = ""
		case s.Cfg.removedMode():
			return s.Cfg.errRemovedMode(s.Dir)
		case mode != "" && mode != was:
			return fail("state_conflict", s.Dir+" is in mint mode "+s.Cfg.mintMode()+"; this tw up asks for "+mode).
				withFix("tw up   (keeps mode "+was+")", "or tw down --purge, remove "+s.Dir+", then tw up with the new mode")
		default:
			mode = was
		}
		if mode == mgmt.MintHeadscale && *control == prodControl {
			*control = s.Cfg.ControlURL // the Headscale server this state dir was set up with
		}
		switch mode {
		case mgmt.MintHeadscale:
			if *keyFile == "" {
				*keyFile = s.Cfg.APIKeyFile
			}
			if *tailnet == "" {
				*tailnet = s.Cfg.Tailnet
			}
		case "oauth":
			if *oauthFile == "" {
				*oauthFile = s.Cfg.OAuthClientFile
			}
		}
		if *tailnet != "" && s.Cfg.Tailnet != *tailnet {
			return fail("state_conflict", s.Dir+" is bound to tailnet "+s.Cfg.Tailnet).
				withFix("tw down --purge, then remove "+s.Dir, "or use another --state-dir")
		}
	}
	defaultDir := isDefaultStateDir(s.Dir, *f.stateDir) // up_defaults.go
	if mode == "" && defaultDir && *oauthFile == "" {
		if p := headscaleDefaultKey(f.FlagSet, *profileF, *control); p != "" { // up_headscale.go
			*keyFile, *profileF, mode = p, controlHeadscale, mgmt.MintHeadscale
		}
	}
	if mode == "" && defaultDir && *profileF != controlHeadscale {
		if p := defaultOAuthFile(); p != "" {
			asked := false
			if !fileExists(p) && !*f.json {
				if asked, err = promptOAuthSecret(p, os.Stdout, "tw up", ""); err != nil {
					return err
				}
			}
			if asked || fileExists(p) {
				*oauthFile, mode = p, "oauth"
			}
		}
	}
	if mode == "" && *profileF == controlHeadscale {
		_, err := headscaleKeyFile("")
		return err
	}
	if mode == "" {
		return mintModeRequired()
	}
	hsMode, oauthMode := mode == mgmt.MintHeadscale, mode == "oauth"
	profile := controlTailscale
	if hsMode {
		profile = controlHeadscale
		if err := checkControlProfile(profile, *control, "up"); err != nil {
			return err
		}
	}
	if hsMode && *tailnet == "" {
		return fail("tailnet_required", "tw up on Headscale needs --tailnet (the Headscale server's dns.base_domain)",
			"an API key does not name its tailnet, and tailworks never uses this machine's tailscaled or its identity.").
			withFix("tw up --control-url <server_url> --tailnet <base_domain> --api-key-file <file with HEADSCALE_API_KEY=…>")
	}
	// oauth: the credential file must be the owner's only (checked before
	// anything else happens; tw-mgmt refuses it too).
	absOAuth := ""
	var oauthCreds tsapi.OAuthCredentials
	if oauthMode {
		absOAuth, _ = filepath.Abs(*oauthFile)
		tightenSecretFile(absOAuth) // your own file: fix its mode instead of refusing it
		c, err := tsapi.LoadOAuthFile(absOAuth)
		if err != nil {
			if tsapi.IsInsecureFile(err) {
				return fail("credential_file_insecure", absOAuth+" is readable by other users", err.Error()).withFix("chmod 600 " + absOAuth + "   then tw up again")
			}
			return fail("bad_request", "can't read the OAuth client from "+absOAuth, netd.Redact(err.Error())).
				withFix("the file needs TS_OAUTH_CLIENT_ID=<id> and TS_OAUTH_CLIENT_SECRET=tskey-client-… (mode 0600)")
		}
		oauthCreds = c
	}
	if err := checkUpLayout(s, absOAuth, *keyFile); err != nil { // sandbox_layout.go (#342)
		return err
	}
	// --service defaults on for the default state dir (where the platform
	// has a user service manager); a state dir already set up keeps how it
	// runs, so tw upgrade's restart doesn't change it.
	if !flagGiven(f.FlagSet, "service") && !*localDev {
		if s.configured() {
			*service, _ = serviceOf(s)
		} else {
			*service = defaultDir && serviceAvailable() == nil && npmServiceErr() == nil // npm_install.go
		}
	}
	if *service {
		if err := serviceAvailable(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// Progress goes to stdout, or to stderr with --json (stdout then
	// carries only the result).
	out := io.Writer(os.Stdout)
	if *f.json {
		out = os.Stderr
	}

	// The credential must work for this tailnet; read the policy (written
	// only with --apply-policy).
	var api *tsapi.Client
	var capi controlAPI // mints mgmt's node key; reads/applies the policy
	hsApplied := false  // headscale: the policy snippet was applied up front
	upApplied := false  // tailscale: --apply-policy wrote the snippet (up_policyapply.go)
	var pol []byte
	policyReadable := false
	absKey := ""
	if hsMode {
		absKey, _ = filepath.Abs(*keyFile)
		ps := time.Now()
		hc, p, err := headscaleClient(ctx, absKey, *control, *tailnet)
		if err != nil {
			return err
		}
		capi, pol, policyReadable = hc, p, true
		timings["api_check"] = ms(time.Since(ps))
		// Headscale refuses to register a node whose tags its policy
		// doesn't define, so the snippet goes in before mgmt's key is used.
		if pol, hsApplied, err = headscaleUpPolicy(ctx, hc, f, pol, pmode, *yes, out); err != nil {
			return err
		}
	}
	// Single-process (the default) needs tag:tw to own tag:tw-mgmt only;
	// tag:tw-host is for `tw host add` (checked below as a warning).
	singleLayout := !(*separate || *localDev)
	if s.configured() {
		singleLayout = s.Cfg.single()
	}
	if oauthMode {
		ps := time.Now()
		src := &tsapi.OAuthSource{BaseURL: *tsapiURL, Load: func() (tsapi.OAuthCredentials, error) { return tsapi.LoadOAuthFile(absOAuth) }}
		tn := *tailnet
		if tn == "" {
			tn = "-" // the token's own tailnet
		}
		api = &tsapi.Client{Tailnet: tn, Tokens: src, BaseURL: *tsapiURL}
		capi = api
		needed := tagOwnersNeeded()
		if singleLayout {
			needed = policy.Snippet{TagOwners: map[string][]string{mgmt.MgmtTag: {"tag:tw"}}}
		}
		pre := needed
		if pmode != policyPrintOnly {
			pre = policy.Snippet{} // upApplyPolicy writes what is missing, then checks needed
		}
		found, p, readable, err := oauthPreflight(ctx, api, src, oauthCreds.ClientID, absOAuth, out, pre)
		if err != nil {
			return err
		}
		pol, policyReadable = p, readable
		if pmode != policyPrintOnly {
			if pol, upApplied, err = upApplyPolicy(ctx, api, f, pol, readable, needed, pmode, *yes, out); err != nil {
				return err
			}
		}
		if *tailnet == "" && found != "" {
			*tailnet = found
		}
		timings["api_check"] = ms(time.Since(ps))
	}

	wd, err := findWorkerd(*workerdFlag)
	if err != nil {
		return err
	}
	if err := upPreflight(wd, out); err != nil { // up_preflight.go (#480)
		return err
	}
	res := upResult{StateDir: s.Dir, HostID: *hostID, Timings: timings}

	// mgmt's own tailnet node carries the API (WhoIs auth, and the local
	// CLI through its loopback). It is the only transport in normal
	// operation; --local-dev may skip it.
	mgmtHostname := *mgmtHost
	if mgmtHostname == "" {
		mgmtHostname = s.Cfg.MgmtHostname
		if mgmtHostname == "" && !*localDev {
			mgmtHostname = defaultMgmtHostname
		}
	}
	if s.configured() && s.Cfg.LocalDev != *localDev {
		return fail("state_conflict", fmt.Sprintf("%s was set up with --local-dev=%v", s.Dir, s.Cfg.LocalDev)).withFix("tw down --purge, remove " + s.Dir + ", then tw up again")
	}
	// The process layout: one process by default (the host agent embedded
	// in mgmt; no host node, no join key); --separate-host (and
	// --local-dev, whose hermetic tests drive the two binaries) keeps
	// tw-mgmt + tw-hostd as children. Existing state keeps its layout.
	layout := ModeSingle
	if *separate || *localDev {
		layout = ModeSeparate
	}
	if s.configured() {
		if *separate && s.Cfg.single() {
			return fail("state_conflict", s.Dir+" runs single-process (the host agent embedded in tw-mgmt); --separate-host needs a fresh state dir").
				withFix("tw down --purge, remove " + s.Dir + ", then tw up --separate-host")
		}
		layout = s.Cfg.modeLabel()
	}
	single := layout == ModeSingle
	res.Mode = layout
	if !single {
		for _, b := range []string{"tw-mgmt", "tw-hostd"} {
			if _, err := findBin(b); err != nil {
				return err
			}
		}
	}
	if s.configured() && s.Cfg.MgmtHostname != "" && mgmtHostname != "" && mgmtHostname != s.Cfg.MgmtHostname {
		return fail("state_conflict", "mgmt's node in "+s.Dir+" is named "+s.Cfg.MgmtHostname).withFix("drop --mgmt-hostname, or use another --state-dir")
	}
	// mgmt joins its own node (up_mgmtjoin.go): on the first start tw up
	// mints the single-use key and hands it over in a 0600 file that mgmt
	// deletes once it has read it. While the supervisor runs, mgmt holds
	// the node and its state is authoritative.
	freshStart := mgmtHostname != "" && !s.supervisorAlive()
	firstUp := !s.configured()
	handedKey := false
	mintMgmtKey := func() error {
		key, err := mintNodeKey(ctx, capi, mgmt.MgmtTag, "tw mgmt node "+mgmtHostname)
		if err != nil {
			return err
		}
		handedKey = true
		return s.writeMgmtJoinKey(key)
	}
	if freshStart {
		defer os.Remove(s.mgmtJoinFile()) // a key mgmt never read doesn't outlive tw up
		os.Remove(s.mgmtStartErr())
		switch {
		case *mgmtKey != "":
			k, err := tsapi.LoadEnvFile(*mgmtKey, "TS_AUTHKEY")
			if err != nil {
				return fail("bad_request", "can't read TS_AUTHKEY from "+*mgmtKey, err.Error())
			}
			if err := s.writeMgmtJoinKey(k); err != nil {
				return err
			}
			handedKey = true
		case s.mgmtNeedsKey():
			// The credential mints mgmt's node key (tag:tw-mgmt): no
			// browser login.
			if err := mintMgmtKey(); err != nil {
				return err
			}
		}
		timings["mgmt_key"] = ms(time.Since(t0))
		s.Cfg.MgmtHostname = mgmtHostname
	} else if mgmtHostname != "" {
		res.MgmtNode, res.MgmtNodeID, res.MgmtIP, res.HTTPS = s.Cfg.MgmtDNSName, s.Cfg.MgmtNodeID, s.Cfg.MgmtIP, s.Cfg.HTTPS
		if *tailnet == "" {
			*tailnet = s.Cfg.Tailnet
		}
	}
	if *tailnet == "" && !freshStart {
		return fail("tailnet_required", "could not determine the tailnet").withFix("tw up --tailnet <tailnet>")
	}
	if !s.configured() {
		s.Cfg = Config{Tailnet: *tailnet, CreatedAt: time.Now().UTC().Format(time.RFC3339), MgmtHostname: s.Cfg.MgmtHostname, MgmtDNSName: s.Cfg.MgmtDNSName,
			MgmtNodeID: s.Cfg.MgmtNodeID, MgmtIP: s.Cfg.MgmtIP, HTTPS: s.Cfg.HTTPS}
	}
	if hsMode {
		s.Cfg.MintMode, s.Cfg.APIKeyFile = mgmt.MintHeadscale, absKey
	} else {
		s.Cfg.MintMode, s.Cfg.OAuthClientFile, s.Cfg.OAuthClientID = "oauth", absOAuth, oauthCreds.ClientID
	}
	res.MintMode = s.Cfg.mintMode()
	s.Cfg.ControlURL, s.Cfg.TSAPIURL, s.Cfg.HostID = *control, *tsapiURL, *hostID
	s.Cfg.HostnamePrefix, s.Cfg.Ephemeral, s.Cfg.Workerd = *prefix, *ephemeral, wd
	s.Cfg.InsecureSharedHost = *insecureSH
	s.Cfg.AllowExperimentalFlags = *allowExp
	s.Cfg.defaultTuning() // up_tuning.go
	s.Cfg.AllowSiblingEgress = *siblings
	if *warmKeys < 0 || *poolNodes < 0 {
		return fail("bad_request", "--warm-keys and --pool-nodes must be ≥ 0")
	}
	s.Cfg.WarmKeys, s.Cfg.PoolNodes, s.Cfg.PortMapping = warmKeys, poolNodes, *portMapping
	if err := retention.save(&s.Cfg); err != nil {
		return err
	}
	if err := saveWorkerInternet(&s.Cfg, *workerInet); err != nil {
		return err
	}
	s.Cfg.LocalDev, s.Cfg.Mode = *localDev, layout
	s.Cfg.InstallPrefix = upInstallPrefix() // upgrade_prefix.go
	if err := storeF.apply(s); err != nil {
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	launch := func() error {
		if *service {
			was := s.supervisorAlive()
			changed, err := installService(s)
			if err != nil {
				return err
			}
			res.Started = !was || changed
			res.Service = serviceUnitPath()
			res.LingerHint = lingerHint()
			res.SleepHint = sleepHint()
		} else if inst, act := serviceOf(s); inst && act {
			return fail("service_running", "tw runs as the "+serviceKind()+" "+serviceUnit+" (installed by tw up --service)").
				withFix("tw up --service … to change its settings", serviceRestartHint(), "tw down --service to remove it")
		} else if !s.supervisorAlive() {
			if err := startSupervisor(s); err != nil {
				return err
			}
			res.Started = true
		}
		return nil
	}
	if err := launch(); err != nil {
		return err
	}
	ms0 := time.Now()
	for !s.mgmtHealthy(ctx) {
		if msg, failed := s.mgmtStartFailure(); failed && freshStart {
			stopFailedStart(s, firstUp) // up_mgmtjoin.go
			if !needsLogin(msg) || handedKey {
				return joinFailure(mgmtHostname, advertisedTags(hsMode, mgmt.MgmtTag), msg)
			}
			// mgmt's node was logged out (removed in the admin console, say):
			// mint a key and start again, once.
			os.Remove(s.mgmtStartErr())
			if err := mintMgmtKey(); err != nil {
				return err
			}
			if err := launch(); err != nil {
				return err
			}
			continue
		}
		if time.Since(ms0) > 90*time.Second || !s.supervisorAlive() {
			if msg, failed := s.mgmtStartFailure(); failed && freshStart {
				stopFailedStart(s, firstUp)
				return joinFailure(mgmtHostname, advertisedTags(hsMode, mgmt.MgmtTag), msg)
			}
			return fail("mgmt_unavailable", "tw-mgmt did not become healthy", "log: "+s.log("mgmt")).withFix("tw logs --component mgmt")
		}
		time.Sleep(20 * time.Millisecond)
	}
	timings["mgmt_ready"] = ms(time.Since(t0))
	if freshStart {
		// mgmt's node as it joined (its loopback descriptor), checked as tw
		// up's own join used to be, then saved.
		lb := s.loopback()
		if lb == nil {
			return fail("mgmt_unavailable", "tw-mgmt is healthy but published no node", "log: "+s.log("mgmt"))
		}
		tn, err := checkMgmtNode(lb, *control, *tailnet)
		if err == nil && tn == "" {
			err = fail("tailnet_required", "could not determine the tailnet").withFix("tw up --tailnet <tailnet>")
		}
		if err != nil {
			stopFailedStart(s, firstUp)
			return err
		}
		*tailnet = tn
		res.MgmtNode, res.MgmtNodeID, res.MgmtIP, res.HTTPS = lb.DNSName, lb.NodeID, lb.MgmtIP, lb.HTTPS
		s.Cfg.Tailnet, s.Cfg.MgmtDNSName, s.Cfg.MgmtNodeID, s.Cfg.MgmtIP, s.Cfg.HTTPS = *tailnet, lb.DNSName, lb.NodeID, lb.MgmtIP, lb.HTTPS
		if err := s.save(); err != nil {
			return err
		}
	}
	res.Tailnet = *tailnet

	// Tenant (operator = tenant = you). In normal operation the host's own
	// node needs a join key minted through the tenant's credential, so the
	// tenant comes first.

	tid := s.Cfg.TenantID
	if tid == "" {
		tid = tenantIDFor(*tailnet)
	}
	{
		want := map[string]any{"mintMode": mgmt.MintHeadscale, "apiKeyFile": absKey}
		if oauthMode {
			want = map[string]any{"mintMode": "oauth", "oauthClientID": oauthCreds.ClientID, "oauthClientFile": absOAuth}
		}
		var cur struct {
			MintMode        string `json:"mintMode"`
			APIKeyFile      string `json:"apiKeyFile"`
			OAuthClientID   string `json:"oauthClientID"`
			OAuthClientFile string `json:"oauthClientFile"`
			PublicIngress   string `json:"publicIngress"`
		}
		if err := s.mgmt().Do(ctx, "GET", "/v1/tenants/"+tid, nil, &cur); err != nil {
			var tr struct {
				TenantID string `json:"tenantID"`
			}
			// Single-tenant tw up: the operator is the tenant, so public
			// ingress is allowed (ADR-029 D1); tailworks.jsonc "public" opts in.
			body := map[string]any{"tenantID": tid, "tailnet": *tailnet, "controlURL": *control, "publicIngress": mgmt.PublicAllow}
			if hsMode {
				body["publicIngress"] = mgmt.PublicDeny // no Funnel on Headscale
			}
			maps.Copy(body, want)
			if err := s.mgmt().Do(ctx, "POST", "/v1/tenants", body, &tr); err != nil {
				return err
			}
			tid = tr.TenantID
		} else {
			patch := map[string]any{}
			if oauthMode && (cur.MintMode != "oauth" || cur.OAuthClientID != oauthCreds.ClientID || cur.OAuthClientFile != absOAuth) {
				// A regenerated client (e.g. one with devices:core added),
				// or a tenant an older tailworks left in a removed mint mode.
				maps.Copy(patch, want)
			}
			if cur.PublicIngress == "" && !hsMode {
				// A tenant from before ADR-029: allow public ingress like a
				// fresh tw up (an explicit "deny" is kept).
				patch["publicIngress"] = mgmt.PublicAllow
			}
			if len(patch) > 0 {
				if err := s.mgmt().Do(ctx, "PATCH", "/v1/tenants/"+tid, patch, nil); err != nil {
					return err
				}
			}
		}
	}
	s.Cfg.TenantID = tid
	if err := s.save(); err != nil {
		return err
	}
	res.TenantID = tid
	timings["tenant_ready"] = ms(time.Since(t0))

	// The host's own node: minted by mgmt (POST /v1/hosts) through the
	// tenant's credential, handed to hostd as a single-use join file.
	if !*localDev && !single && !isFile(filepath.Join(s.hostTsnetDir(), "tailscaled.state")) {
		js := time.Now()
		if err := s.mintHostJoin(ctx, *hostID, tid); err != nil {
			return err
		}
		timings["host_join_key"] = ms(time.Since(js))
	}
	// Host registered and heartbeating.
	hs := time.Now()
	for {
		var h struct {
			ID       string `json:"hostID"`
			LastSeen int64  `json:"lastSeen"`
			Status   string `json:"status"`
		}
		if s.mgmt().Do(ctx, "GET", "/v1/hosts/"+*hostID, nil, &h) == nil && h.Status == "live" {
			if s.hostd().Do(ctx, "GET", "/v1/status", nil, nil) == nil {
				break
			}
		}
		if time.Since(hs) > 3*time.Minute || !s.supervisorAlive() {
			return fail("host_unavailable", "the host agent did not register with tw-mgmt", "log: "+s.log("hostd")).withFix("tw logs --component hostd")
		}
		time.Sleep(100 * time.Millisecond)
	}
	timings["host_ready"] = ms(time.Since(t0))

	if hsMode {
		snip, popts := profileSnippet(profile)
		miss, err := policy.Missing(pol, snip)
		if err != nil {
			return err
		}
		res.PolicyOK = miss.Empty()
		if !res.PolicyOK {
			res.PolicyDiff = miss.HuJSON()
			write, err := bootstrapPolicyGate(ctx, capi, f, pmode, *yes, "tw up", profile, pol, snip, popts, miss, out) // policy_ask.go
			if err != nil {
				return err
			}
			if write {
				ar, err := policy.ApplyWith(ctx, capi, snip, popts)
				if err != nil {
					return fail("policy_apply_failed", "could not apply the policy snippet", err.Error()).withFix("add it in "+policyHome(profile)+":", res.PolicyDiff)
				}
				res.Applied, res.PolicyOK = ar.Changed, true
			}
		}
		res.Applied = res.Applied || hsApplied
	} else {
		res.Applied = upApplied
		// Tag ownership is proven (mgmt's and the host's keys minted); the
		// grants are checked when the client can read the policy.
		res.PolicyOK = true
		if policyReadable {
			// Grants only: tag ownership is proven by the mints, and other
			// owners of tag:tw (a group, say) are fine. Single-process:
			// the host key was not minted, so tag:tw-host ownership is
			// checked here too, and everything about tag:tw-host is
			// advisory (needed only for tw host add).
			want := policy.Snippet{Grants: operatorSnippetFor(pol).Grants}
			if single {
				want.TagOwners = map[string][]string{mgmt.HostTag: {"tag:tw"}}
			}
			if miss, err := policy.Missing(pol, want); err == nil && !miss.Empty() {
				if single && hostOnly(miss) {
					res.ScaleOut = miss.HuJSON()
				} else {
					res.PolicyOK, res.PolicyDiff = false, miss.HuJSON()
				}
			}
		}
	}
	timings["total"] = ms(time.Since(t0))
	res.UnsafeFlags = unsafeFlags(s.Cfg) // up_unsafe.go
	emit(f, res, func() {
		if hsMode {
			fmt.Printf("tw up · all-in-one on this machine (%s) · Headscale %s, base domain %s (API key, mint mode headscale)\n", layoutLabel(single), *control, *tailnet)
		} else {
			fmt.Printf("tw up · all-in-one on this machine (%s) · tailnet: %s (OAuth client %s, mint mode oauth)\n", layoutLabel(single), *tailnet, oauthCreds.ClientID)
		}
		started := "already running"
		if res.Started {
			started = "started"
		}
		mgmtName := "tw-mgmt"
		if single {
			mgmtName = "tw-mgmt (in the tailworks supervisor process)"
		}
		fmt.Printf("✓ mgmt     %s %s · %s\n", mgmtName, started, s.Cfg.storeLabel(s.Dir))
		if res.MgmtNode != "" {
			sch := "http"
			if res.HTTPS {
				sch = "https"
			}
			fmt.Printf("✓ node     %s (%s) · API on the tailnet: %s://%s (WhoIs auth; `tw link %s` from your machine)\n", res.MgmtNode, mgmt.MgmtTag, sch, res.MgmtNode, res.MgmtNode)
			fmt.Printf("✓ console  %s://%s/ui/ (admin UI; any tailnet admin's browser, WhoIs auth)\n", sch, res.MgmtNode)
		}
		if single {
			fmt.Printf("✓ host     this machine (%s) · embedded in tw-mgmt (single process; `tw up --separate-host` or `tw host add` for more hosts) · %s\n", *hostID, tierLine(s))
		} else {
			fmt.Printf("✓ host     this machine (%s) · own node %s (%s) · %s\n", *hostID, mgmt.HostNodeName(*hostID), mgmt.HostTag, tierLine(s))
		}
		printSavedFlags(s.Cfg, s.Dir, 9, nil) // up_unsafe.go
		fmt.Printf("✓ tenant   %s connected (tenant %s, mint mode %s) · control %s\n", *tailnet, tid, s.Cfg.mintMode(), *control)
		switch {
		case res.Applied && oauthMode:
			fmt.Printf("✓ policy   applied tailworks' tagOwners and grants (--apply-policy; validated, If-Match)\n")
		case res.Applied:
			fmt.Printf("✓ policy   applied tag:tw tagOwners (validated, If-Match)\n")
		case res.PolicyOK && hsMode:
			fmt.Printf("✓ policy   tag:tw tagOwners present\n")
		case res.PolicyOK && oauthMode && policyReadable && res.ScaleOut != "":
			fmt.Printf("✓ policy   tag:tw owns %s; admin grant present\n", mgmt.MgmtTag)
			fmt.Printf("› policy   scale-out (`tw host add`) will also need %s owned by tag:tw and the host ⇄ mgmt grants; not needed for this single-process host:\n", mgmt.HostTag)
			for _, l := range strings.Split(strings.TrimSpace(res.ScaleOut), "\n") {
				fmt.Printf("    %s\n", l)
			}
		case res.PolicyOK && oauthMode && policyReadable:
			fmt.Printf("✓ policy   tag:tw owns %s and %s; host ⇄ mgmt and admin grants present\n", mgmt.MgmtTag, mgmt.HostTag)
		case res.PolicyOK && oauthMode && single:
			fmt.Printf("✓ policy   tag:tw owns %s (key minted); grants and %s ownership not checked (the OAuth client can't read the policy; %s matters only for tw host add)\n", mgmt.MgmtTag, mgmt.HostTag, mgmt.HostTag)
		case res.PolicyOK && oauthMode:
			fmt.Printf("✓ policy   tag:tw owns %s and %s (both keys minted); grants not checked (the OAuth client can't read the policy)\n", mgmt.MgmtTag, mgmt.HostTag)
		default:
			if oauthMode {
				fmt.Printf("› policy   missing (re-run with --apply-policy if the OAuth client has Policy File › Write, or add in the admin console › Access controls):\n")
			} else {
				fmt.Printf("› policy   not applied (re-run with --apply-policy, or add in %s):\n", policyHome(profile))
			}
			for _, l := range strings.Split(strings.TrimSpace(res.PolicyDiff), "\n") {
				fmt.Printf("    %s\n", l)
			}
		}
		if hsMode {
			fmt.Printf("› profile  %s\n", headscaleProfileNote)
		}
		if res.Service != "" {
			fmt.Printf("✓ service  %s %s (starts at login; `tw down --service` removes it)\n", serviceKind(), res.Service)
			if res.LingerHint != "" {
				fmt.Printf("› linger   %s\n", res.LingerHint)
			}
			if res.SleepHint != "" {
				fmt.Printf("› sleep    %s\n", res.SleepHint)
			}
		}
		fmt.Printf("ready in %.1fs · `tw init hello --template agent && cd hello && tw deploy`\n", timings["total"]/1000)
	})
	return nil
}

// startSupervisor starts `tw __supervise` detached (its own session), with
// output in <state>/logs/supervisor.log.
func startSupervisor(s *state) error {
	if err := os.MkdirAll(s.logDir(), 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	lf, err := os.OpenFile(s.log("supervisor"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd := exec.Command(exe, "__supervise", "--state-dir", s.Dir)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = lf, lf, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	for i := 0; i < 100; i++ {
		if s.supervisorPID() == pid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !pidAlive(pid) {
		return fail("supervisor_failed", "tw supervisor exited at start", "log: "+s.log("supervisor"))
	}
	return nil
}

// layoutLabel names the process layout in `tw up` output.
func layoutLabel(single bool) string {
	if single {
		return "one process: mgmt + embedded host"
	}
	return "two processes: tw-mgmt + tw-hostd"
}

// hostOnly reports whether every missing policy entry is about
// tag:tw-host (its tagOwners entry, or a grant with it as src or dst):
// in single-process mode those matter only for `tw host add`.
func hostOnly(miss policy.Snippet) bool {
	for tag := range miss.TagOwners {
		if tag != mgmt.HostTag {
			return false
		}
	}
	for _, g := range miss.Grants {
		found := false
		for _, k := range []string{"src", "dst"} {
			if vs, ok := g[k].([]string); ok && slices.Contains(vs, mgmt.HostTag) {
				found = true
			}
			if vs, ok := g[k].([]any); ok && slices.Contains(vs, any(mgmt.HostTag)) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return len(miss.NodeAttrs) == 0
}

// ---- supervisor ----

type child struct {
	name    string
	args    []string
	logPath string

	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	restarts []time.Time
}

func (c *child) start(slog func(string, ...any)) error {
	lf, err := openDaemonLog(c.logPath) // up_logs.go: rotated
	if err != nil {
		return err
	}
	cmd := exec.Command(c.args[0], c.args[1:]...)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	done := make(chan struct{})
	c.mu.Lock()
	c.cmd, c.done = cmd, done
	c.mu.Unlock()
	slog("started %s pid %d", c.name, cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		lf.Close()
		slog("%s exited: %v", c.name, err)
		close(done)
	}()
	return nil
}

func (c *child) stop(timeout time.Duration) {
	c.mu.Lock()
	cmd, done := c.cmd, c.done
	c.mu.Unlock()
	if cmd == nil {
		return
	}
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
	}
}

func cmdSupervise(args []string) error {
	f := newFlags("__supervise")
	if _, err := f.parse(args); err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if !s.configured() && !s.firstStartPending() { // up_mgmtjoin.go
		return fail("not_up", "no config in "+s.Dir)
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s supervisor: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
	if pidAlive(s.supervisorPID()) && s.supervisorPID() != os.Getpid() {
		return fail("already_running", "a supervisor is already running")
	}
	if err := os.WriteFile(s.pidFile(), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		return err
	}
	defer os.Remove(s.pidFile())
	for _, d := range []string{s.runDir(), s.hostdRunDir(), filepath.Join(s.Dir, "mgmt"), filepath.Join(s.Dir, "hostd"), s.logDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		os.Chmod(d, 0o700)
	}
	if s.Cfg.single() {
		return superviseSingle(s, logf)
	}
	mgmtBin, err := findBin("tw-mgmt")
	if err != nil {
		return err
	}
	hostdBin, err := findBin("tw-hostd")
	if err != nil {
		return err
	}
	margs := []string{mgmtBin, "-state-dir", filepath.Join(s.Dir, "mgmt"), "-device-delete-delay", s.Cfg.DeviceDeleteDelay, "-version", version}
	margs = append(margs, s.Cfg.identityMgmtArgs()...)
	margs = append(margs, s.Cfg.storeMgmtArgs()...)
	margs = append(margs, s.Cfg.retentionMgmtArgs()...) // up_retention.go
	margs = append(margs, "-revoke-warm-keys-on-exit")  // tw down leaves no preauthorized key in custody (#158)
	if s.Cfg.LocalDev {
		// RT-03: a fresh per-host token on every mgmt start (tw up, a
		// supervisor/systemd restart). hostd re-reads the file when it
		// changes and the CLI reads it per call, so rotation never strands
		// a running hostd.
		margs = append(margs, "-local-dev", "-socket", s.mgmtSock(), "-host-token-file", s.hostTokenFile(), "-rotate-host-token")
	}
	if s.Cfg.TSAPIURL != "" {
		margs = append(margs, "-tsapi-url", s.Cfg.TSAPIURL)
	}
	if s.Cfg.MgmtHostname != "" {
		// mgmt's own tailnet node (it joins with tw up's key): the API over WhoIs
		// (never Funnel).
		margs = append(margs, "-tsnet-hostname", s.Cfg.MgmtHostname, "-tsnet-control-url", s.Cfg.ControlURL)
		margs = append(margs, "-tsnet-tags="+strings.Join(advertisedTags(s.Cfg.headscale(), mgmt.MgmtTag), ","))
		// tw up's single-use join key (up_mgmtjoin.go), when there is one.
		margs = append(margs, "-tsnet-authkey-file", s.mgmtJoinFile(), "-tsnet-authkey-once")
	}
	mgmtURLArg := 0 // hargs index of -mgmt's value (normal operation)
	hargs := []string{hostdBin, "-host-id", s.Cfg.HostID,
		"-data-dir", filepath.Join(s.Dir, "hostd"), "-run-dir", s.hostdRunDir(), "-workerd", s.Cfg.Workerd,
		"-heartbeat", s.Cfg.Heartbeat, "-kill-tombstone", s.Cfg.KillTombstone, "-hostname-prefix", s.Cfg.HostnamePrefix,
		// No tenant's workerd may touch tailworks' state (mgmt custody, every
		// tenant's node keys and DO storage) or its sockets, except its own.
		"-sandbox", "auto", "-sandbox-deny", strings.Join(s.sandboxDenyList(), ",")} // sandbox_layout.go
	if s.Cfg.LocalDev {
		hargs = append(hargs, "-local-dev", "-mgmt", "unix://"+s.mgmtSock(), "-mgmt-token-file", s.hostTokenFile())
	} else {
		// Normal operation: hostd's own node, mgmt over the tailnet (by
		// tailnet IP: a MagicDNS name resolves only once the peer is in the
		// netmap, and mgmt's node keeps its IP across restarts).
		mgmtURL := "http://" + s.Cfg.MgmtIP
		if s.Cfg.MgmtIP == "" {
			mgmtURL = "http://" + s.Cfg.MgmtDNSName
		}
		mgmtURLArg = len(hargs) + 1
		port := s.Cfg.PushPort
		if port == 0 {
			port = defaultPushPort
		}
		hargs = append(hargs, "-mgmt", mgmtURL, "-tsnet-control-url", s.Cfg.ControlURL, "-tsnet-dir", s.hostTsnetDir(),
			"-join-token-file", s.hostJoinFile(), "-push-port", strconv.Itoa(port))
		if s.Cfg.headscale() {
			hargs = append(hargs, "-tsnet-key-tags-only")
		}
	}
	if s.Cfg.Ephemeral {
		hargs = append(hargs, "-ephemeral")
	}
	if s.Cfg.InsecureSharedHost {
		hargs = append(hargs, "-insecure-shared-host")
	}
	if s.Cfg.AllowExperimentalFlags {
		hargs = append(hargs, "-allow-experimental-flags")
	}
	if s.Cfg.AllowSiblingEgress {
		hargs = append(hargs, "-allow-sibling-egress")
	}
	hargs = append(hargs, s.Cfg.identityHostdArgs()...)
	hargs = append(hargs, s.Cfg.retentionHostdArgs()...) // up_retention.go
	hargs = append(hargs, s.Cfg.workerInternetHostdArgs()...)
	mg := &child{name: "tw-mgmt", args: margs, logPath: s.log("mgmt")}
	hd := &child{name: "tw-hostd", args: hargs, logPath: s.log("hostd")}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	st := newSupStop() // supervise_exit.go: a signal exits 0, a child giving up exits 1
	stopping := st.done
	go func() {
		<-sig
		st.stop(nil)
	}()
	waitMgmt := func() bool {
		for i := 0; i < 600; i++ {
			if s.mgmtHealthy(context.Background()) {
				return true
			}
			select {
			case <-stopping:
				return false
			case <-time.After(50 * time.Millisecond):
			}
		}
		return false
	}
	// supervise runs c, restarting it with backoff (≤10 restarts per 5 min).
	var wg sync.WaitGroup
	supervise := func(c *child, before func() bool) {
		defer wg.Done()
		backoff := 500 * time.Millisecond
		for {
			if before != nil && !before() {
				return
			}
			if err := c.start(logf); err != nil {
				logf("start %s: %v", c.name, err)
			} else {
				st := time.Now()
				select {
				case <-c.done:
				case <-stopping:
					return
				}
				if time.Since(st) > time.Minute {
					backoff = 500 * time.Millisecond
				}
			}
			now := time.Now()
			c.restarts = append(c.restarts, now)
			for len(c.restarts) > 0 && now.Sub(c.restarts[0]) > 5*time.Minute {
				c.restarts = c.restarts[1:]
			}
			if len(c.restarts) > 10 {
				logf("%s keeps exiting; giving up (see %s)", c.name, c.logPath)
				st.stop(gaveUpErr(c))
				return
			}
			select {
			case <-stopping:
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
	logf("supervising in %s (tailnet %s)", s.Dir, s.Cfg.Tailnet)
	wg.Add(2)
	go supervise(mg, nil)
	go supervise(hd, func() bool {
		if !waitMgmt() {
			return false
		}
		// mgmt's tailnet IP from its loopback descriptor: on tw up's first
		// start, mgmt joins its node after this supervisor started.
		if lb := s.loopback(); lb != nil && mgmtURLArg > 0 {
			hd.args[mgmtURLArg] = "http://" + lb.MgmtIP
		}
		return true
	})
	<-stopping
	logf("stopping: hostd first (nodes close in parallel), then mgmt (flushes pending device deletes)")
	hd.stop(45 * time.Second)
	mg.stop(90 * time.Second)
	wg.Wait()
	logf("stopped")
	return st.err
}

// superviseSingle is the single-process layout: mgmt (internal/mgmtd) and
// the host agent (internal/hostdrun) in this process. mgmt's node is the
// only infrastructure device; hostd talks to mgmt through the in-process
// transport (internal/inproc: the host principal, no listener) and mgmt
// pushes kills through hostd's handler. Each component logs to its own
// file (<state>/logs/mgmt.log, hostd.log) so `tw logs --component` is
// unchanged. SIGTERM stops hostd first (nodes close in parallel), then
// mgmt (flushes pending device deletes).
func superviseSingle(s *state, logf func(string, ...any)) error {
	openLog := func(name string) (io.WriteCloser, *slog.Logger, error) {
		lf, err := openDaemonLog(s.log(name)) // up_logs.go: rotated
		if err != nil {
			return nil, nil, err
		}
		return lf, slog.New(netd.NewRedactingHandler(slog.NewTextHandler(lf, &slog.HandlerOptions{Level: slog.LevelInfo}))), nil
	}
	mlf, mlog, err := openLog("mgmt")
	if err != nil {
		return err
	}
	defer mlf.Close()
	hlf, hlog, err := openLog("hostd")
	if err != nil {
		return err
	}
	defer hlf.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	delDelay, _ := time.ParseDuration(s.Cfg.DeviceDeleteDelay)
	mo := mgmtd.Options{StateDir: filepath.Join(s.Dir, "mgmt"), DeviceDeleteDelay: delDelay, Version: version, TSAPIURL: s.Cfg.TSAPIURL,
		TSNetHostname: s.Cfg.MgmtHostname, TSNetControlURL: s.Cfg.ControlURL, TSNetTags: advertisedTags(s.Cfg.headscale(), mgmt.MgmtTag), TSNetTLS: true,
		TSNetAuthKeyFile: s.mgmtJoinFile(), TSNetAuthKeyOnce: true, // tw up's single-use join key, when there is one (up_mgmtjoin.go)
		Log: mlog, RevokeWarmKeysOnClose: true, // tw down leaves no preauthorized key in custody (#158)
		MetricsPort: obs.DefaultMetricsPort}
	if mo.TSAPIURL == "" {
		mo.TSAPIURL = tsapi.DefaultBaseURL
	}
	s.Cfg.storeOptions(&mo)         // up_store.go
	s.Cfg.retentionSingle(&mo, nil) // up_retention.go
	s.Cfg.identitySingle(&mo, nil)  // up_identity.go
	if !s.Cfg.PortMapping {
		netd.DisablePortMapping() // process-wide: before mgmt's node exists
	}
	logf("supervising in %s (tailnet %s): single process, mgmt + embedded host %s", s.Dir, s.Cfg.Tailnet, s.Cfg.HostID)
	mrt, err := mgmtd.Start(ctx, mo)
	if err != nil {
		logf("mgmt failed to start: %v", err)
		return fail("mgmt_failed", "tw-mgmt (embedded) failed to start", netd.Redact(err.Error()))
	}
	defer mrt.Close()
	hb, _ := time.ParseDuration(s.Cfg.Heartbeat)
	tomb, _ := time.ParseDuration(s.Cfg.KillTombstone)
	// No tenant's workerd may touch tailworks' state (mgmt custody, every
	// tenant's node keys and DO storage) or its sockets, except its own.
	deny := s.sandboxDenyList() // sandbox_layout.go
	ho := hostdrun.Options{Client: inproc.NewClient(mrt.Handler(), s.Cfg.HostID), HostID: s.Cfg.HostID,
		DataDir: filepath.Join(s.Dir, "hostd"), RunDir: s.hostdRunDir(), Workerd: s.Cfg.Workerd,
		Heartbeat: hb, KillTombstone: tomb, HostnamePrefix: s.Cfg.HostnamePrefix, Sandbox: "auto", SandboxDeny: deny,
		Ephemeral: s.Cfg.Ephemeral, InsecureSharedHost: s.Cfg.InsecureSharedHost, AllowSiblingEgress: s.Cfg.AllowSiblingEgress, Log: hlog}
	// Experimental compatibility flags (hostd/gen/compatflags.go, #337).
	ho.AllowExperimentalFlags = s.Cfg.AllowExperimentalFlags
	// The converted container image cache cap, tw-hostd's default (#476).
	ho.ContainerImageCacheMaxMiB = hostdrun.DefaultImageCacheMiB
	s.Cfg.retentionSingle(nil, &ho) // up_retention.go
	s.Cfg.identitySingle(nil, &ho)  // up_identity.go
	s.Cfg.workerInternetSingle(&ho) // up_workerinternet.go
	hrt, err := hostdrun.New(ctx, ho)
	if err != nil {
		logf("host agent failed to start: %v", err)
		return fail("host_failed", "the embedded host agent failed to start", netd.Redact(err.Error()))
	}
	// mgmt serves this host's pushes through its handler; the host then
	// registers with push URL inproc:// (accepted only with the in-process
	// marker the transport sets).
	mrt.M.SetInProcessPush(s.Cfg.HostID, hrt.Host.PushHandler())
	mrt.AddMetrics(hrt.Host.Metrics()) // the host has no node of its own: /metrics on mgmt's
	if err := hrt.Start(ctx); err != nil {
		hrt.Close()
		logf("host agent failed to register: %v", err)
		return fail("host_failed", "the embedded host agent could not register with mgmt", netd.Redact(err.Error()))
	}
	logf("running: mgmt node %s, host %s embedded (transport %s)", mrt.Self.DNSName, s.Cfg.HostID, hrt.Transport)
	werr := waitSingle(ctx, mrt.Errs(), logf) // supervise_exit.go: exit 1 on a listener failure (#110)
	hrt.Close()
	mrt.Close()
	logf("stopped")
	return werr
}

// ---- down ----

type downResult struct {
	ServiceRemoved bool               `json:"serviceRemoved,omitempty"`
	ServiceStopped bool               `json:"serviceStopped,omitempty"`
	Purged         int                `json:"objectsPurged"`
	Killed         []string           `json:"killed"`
	Flushed        int                `json:"devicesDeleted"`
	Timings        map[string]float64 `json:"timingsMs"`
}

func cmdDown(args []string) error {
	f := newFlags("down")
	service := f.Bool("service", false, "also disable and remove the service installed by tw up --service (systemd user unit / macOS LaunchAgent)")
	purge := f.Bool("purge", false, "kill every live object (audit: \"tw down --purge\") and delete its device before stopping (default with --ephemeral)")
	if _, err := f.parse(args); err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	t0 := time.Now()
	res := downResult{Killed: []string{}, Timings: map[string]float64{}}
	if !s.supervisorAlive() {
		if *service && serviceIsFor(s) {
			removed, err := removeService()
			if err != nil {
				return err
			}
			res.ServiceRemoved = removed
		}
		emit(f, res, func() {
			fmt.Println("tw is not running in " + s.Dir)
			if res.ServiceRemoved {
				fmt.Println("✓ removed  " + serviceUnitPath())
			}
		})
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if (*purge || s.Cfg.Ephemeral) && s.Cfg.TenantID != "" && s.mgmtHealthy(ctx) {
		downPurge(ctx, s, &res)
		res.Timings["purge"] = ms(time.Since(t0))
	}
	if err := downStop(s, *service, &res, t0); err != nil {
		return err
	}
	res.Timings["total"] = ms(time.Since(t0))
	emit(f, res, func() {
		if len(res.Killed) > 0 || res.Purged > 0 {
			fmt.Printf("✓ purged   %d objects killed · %d devices deleted · %d killed objects forgotten\n", len(res.Killed), res.Flushed, res.Purged)
		}
		what := "tw-hostd, tw-mgmt"
		if s.Cfg.single() {
			what = "tw-mgmt with the embedded host"
		}
		fmt.Printf("✓ stopped  %s (%.1fs) · state kept in %s\n", what, res.Timings["total"]/1000, s.Dir)
		if res.ServiceRemoved {
			fmt.Println("✓ removed  " + serviceUnitPath() + " (disabled)")
		} else if res.ServiceStopped {
			fmt.Println("› service  " + serviceUnit + " stopped but still installed (starts at next login/boot); `tw down --service` removes it")
		}
	})
	return nil
}

// downPurge is tw down --purge's first half (also tw uninstall's): kill
// every live object of every tenant this host serves, delete their devices
// and forget them. mgmt must be healthy.
func downPurge(ctx context.Context, s *state, res *downResult) {
	// No new objects from here on (no pool refill, no create from a
	// request arriving meanwhile) and none in flight, so the list below
	// is complete (hostd/teardown.go). An older hostd without the route
	// answers 404: carry on as before.
	s.hostd().Do(ctx, "POST", "/v1/teardown", nil, nil)
	// Every tenant this host serves (each kill goes to its own tailnet).
	var objs []mgmt.Object
	for _, tn := range s.tenants() {
		o, err := s.objectsOf(ctx, tn.TenantID)
		if err == nil {
			objs = append(objs, o...)
		}
	}
	{
		live := map[string]bool{}
		for _, o := range objs {
			if o.Status != "killed" && o.Status != "failed" && o.Status != "transferred" {
				live[o.ID] = true
			}
		}
		for _, o := range objs {
			if !live[o.ID] || live[o.ParentID] {
				continue // dead, or killed with its root (cascade)
			}
			var kr struct {
				Killed []string `json:"killed"`
			}
			if err := s.mgmt().DoHeaders(ctx, "DELETE", "/v1/objects/"+o.ID+"?cascade=true&reason="+url.QueryEscape("tw down --purge"), nil,
				&kr, map[string]string{"Tw-Actor": actor()}); err == nil {
				res.Killed = append(res.Killed, kr.Killed...)
			}
		}
	}
	var fr struct {
		Deleted []string `json:"deleted"`
	}
	s.mgmt().Do(ctx, "POST", "/v1/device-delete-runs", nil, &fr)
	res.Flushed = len(fr.Deleted)
	// Forget the killed objects (mgmt rows, hostd tombstones) so the
	// same workload deploys again on this state dir.
	for _, tn := range s.tenants() {
		var pr mgmt.PurgeResult
		if err := s.mgmt().Do(ctx, "DELETE", "/v1/tenants/"+tn.TenantID+"/objects", nil, &pr); err == nil {
			res.Purged += len(pr.Purged)
		}
	}
	s.hostd().Do(ctx, "POST", "/v1/purge", nil, nil)
}

// downStop stops the supervisor (through the service manager when one runs
// it; service: also remove the service) and waits for it to exit.
func downStop(s *state, service bool, res *downResult, t0 time.Time) error {
	pid := s.supervisorPID()
	inst, act := serviceOf(s) // never another state dir's service (e.g. the real ~/.tailworks while tests run)
	switch {
	case service && inst:
		// disable --now stops it (the supervisor's own SIGTERM path) and
		// keeps it from starting again at login/boot.
		removed, err := removeService()
		if err != nil {
			return err
		}
		res.ServiceRemoved = removed
	case inst && act:
		// Stop through the service manager so it doesn't count as a
		// failure; the unit stays installed (and enabled: it starts at the
		// next login/boot).
		if err := serviceStop(); err != nil {
			return err
		}
		res.ServiceStopped = true
	default:
		syscall.Kill(pid, syscall.SIGTERM)
	}
	for pidAlive(pid) {
		if time.Since(t0) > 3*time.Minute {
			syscall.Kill(pid, syscall.SIGKILL)
			return fail("stop_timeout", "the supervisor did not stop in 3 min; killed it", "log: "+s.log("supervisor"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func actor() string {
	u := os.Getenv("USER")
	if u == "" {
		u = "local"
	}
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return u + "@" + h + " (tw cli)"
}

// ---- status ----

type tenantStatus struct {
	Tailnet   string         `json:"tailnet"`
	TenantID  string         `json:"tenantID"`
	MintQueue int            `json:"mintQueue"`
	Rate      float64        `json:"registrationsPerMinute"`
	Objects   map[string]int `json:"objectsByStatus"`

	// WarmKeys: pooled preauthorized join keys in mgmt custody (#158).
	WarmKeys int `json:"warmKeys"`
}

type statusResult struct {
	Transport  string                 `json:"transport"`
	Mode       string                 `json:"mode"`
	HostStatus string                 `json:"hostStatus,omitempty"`
	StateDir   string                 `json:"stateDir"`
	Tailnet    string                 `json:"tailnet"`
	MintMode   string                 `json:"mintMode"`
	MgmtNode   string                 `json:"mgmtNode,omitempty"`
	TenantID   string                 `json:"tenantID"`
	Tenants    []tenantStatus         `json:"tenants"`
	MgmtSocket string                 `json:"mgmtSocket"`
	Supervisor bool                   `json:"supervisor"`
	Mgmt       bool                   `json:"mgmt"`
	Host       bool                   `json:"host"`
	HostAgeMS  int64                  `json:"hostHeartbeatAgeMs"`
	Pending    int                    `json:"pendingDeviceDeletes"`
	MintQueue  int                    `json:"mintQueue"`
	Rate       float64                `json:"registrationsPerMinute"`
	Workloads  []hostd.WorkloadStatus `json:"workloads"`
	Objects    map[string]int         `json:"objectsByStatus"`
	Healthy    bool                   `json:"healthy"`
	// Containers (ADR-032): this host's containers, mgmt's reservations
	// and every host's capacity vector and usage.
	Containers   []hostd.ContainerStatus     `json:"containers,omitempty"`
	Reservations []mgmt.ContainerReservation `json:"containerReservations,omitempty"`
	Capacity     []hostCapacity              `json:"hostCapacity,omitempty"`

	// DegradedStores (#147, storedegraded.go): stores whose object stopped
	// its replay at a journal entry it can't apply.
	DegradedStores []mgmt.StoreInfo `json:"degradedStores,omitempty"`

	// WarmKeys: the operator tenant's pooled preauthorized join keys in
	// mgmt custody (#158; per tenant in Tenants).
	WarmKeys int `json:"warmKeys"`

	// WorkerInternet: this host's Worker internet egress, "direct" or
	// "off" (up_workerinternet.go): what the running host reports, else
	// tw.json's.
	WorkerInternet string `json:"workerInternet"`

	// UnsafeFlags: the saved unsafe tw up flags (up_unsafe.go).
	UnsafeFlags []string `json:"unsafeFlags"`
}

func cmdStatus(args []string) error {
	f := newFlags("status")
	if _, err := f.parse(args); err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if s.remote() {
		return remoteStatus(f, s)
	}
	if !s.configured() {
		return fail("not_up", "tw is not set up in "+s.Dir).withFix("tw up   (on the host)", "tw link <tw-mgmt.<tailnet>.ts.net>   (on a developer machine)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := statusResult{StateDir: s.Dir, Tailnet: s.Cfg.Tailnet, TenantID: s.Cfg.TenantID, Objects: map[string]int{}, MgmtSocket: s.mgmtSock(),
		MintMode: s.Cfg.mintMode(), MgmtNode: s.Cfg.MgmtDNSName, Mode: s.Cfg.modeLabel()}
	r.Supervisor = s.supervisorAlive()
	if r.Supervisor && s.mgmtHealthy(ctx) {
		r.Mgmt = true
		var st struct {
			Pending int `json:"pendingDeviceDeletes"`
		}
		s.mgmt().Do(ctx, "GET", "/v1/status", nil, &st)
		r.Pending = st.Pending
		var tn struct {
			Q    int     `json:"mintQueue"`
			Rate float64 `json:"effectiveRegistrationsPerMinute"`
			Warm int     `json:"warmKeys"`
		}
		s.mgmt().Do(ctx, "GET", "/v1/tenants/"+s.Cfg.TenantID, nil, &tn)
		r.MintQueue, r.Rate, r.WarmKeys = tn.Q, tn.Rate, tn.Warm
		var h struct {
			ID       string `json:"hostID"`
			LastSeen int64  `json:"lastSeen"`
			Status   string `json:"status"`
		}
		if s.mgmt().Do(ctx, "GET", "/v1/hosts/"+s.Cfg.HostID, nil, &h) == nil {
			r.HostAgeMS = time.Since(time.UnixMilli(h.LastSeen)).Milliseconds()
			r.Host = h.Status == "live"
			r.HostStatus = h.Status
		} else if !s.Cfg.LocalDev && !s.Cfg.single() && !isFile(filepath.Join(s.hostTsnetDir(), "tailscaled.state")) {
			r.HostStatus = "pending" // hostd waits for its join key
		}
	}
	r.Transport = s.transport()
	var hv hostd.StatusView
	hostOK := r.Supervisor && s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv) == nil
	if hostOK {
		r.Workloads = hv.Workloads
		for _, o := range hv.Objects {
			r.Objects[o.Status]++
		}
	} else {
		r.Host = false
	}
	for _, t := range s.tenants() {
		ts := tenantStatus{Tailnet: t.Tailnet, TenantID: t.TenantID, Objects: map[string]int{}}
		if r.Mgmt {
			var tn struct {
				Q    int     `json:"mintQueue"`
				Rate float64 `json:"effectiveRegistrationsPerMinute"`
				Warm int     `json:"warmKeys"`
			}
			s.mgmt().Do(ctx, "GET", "/v1/tenants/"+t.TenantID, nil, &tn)
			ts.MintQueue, ts.Rate, ts.WarmKeys = tn.Q, tn.Rate, tn.Warm
		}
		for _, o := range hv.Objects {
			if o.TenantID == t.TenantID {
				ts.Objects[o.Status]++
			}
		}
		r.Tenants = append(r.Tenants, ts)
	}
	r.Containers = hv.Containers
	r.WorkerInternet = cmpOr(hv.WorkerInternet, s.Cfg.workerInternet())
	if r.Mgmt {
		r.Capacity, r.Reservations = containerState(ctx, s.mgmt(), "")
		r.DegradedStores = degradedStores(ctx, s.mgmt(), "")
	}
	r.Healthy = r.Supervisor && r.Mgmt && r.Host
	r.UnsafeFlags = unsafeFlags(s.Cfg)
	if r.Workloads == nil {
		r.Workloads = []hostd.WorkloadStatus{} // [], never null (docs/cli-json.md)
	}
	emit(f, r, func() { printHostStatus(s, r, hv, hostOK) }) // status_view.go
	if !r.Healthy {
		return fail("unhealthy", "tw is not healthy in "+s.Dir).withFix("tw logs --component supervisor", "tw up")
	}
	return nil
}

// remoteStatus is `tw status` on a linked machine: who we are to mgmt,
// its tenants and hosts, and the latest deployments.
func remoteStatus(f *flags, s *state) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w, err := s.whoami(ctx)
	if err != nil {
		return err
	}
	hosts, _ := listAll[mgmt.Host](ctx, s.mgmt(), "/v1/hosts", 0)
	tenants := []remoteTenant{}
	for _, t := range s.tenants() {
		tv := remoteTenant{TenantRef: t}
		tv.Deployments, _ = listAll[mgmt.DeploymentView](ctx, s.mgmt(), "/v1/deployments?latest=true&tenant="+url.QueryEscape(t.TenantID), 0)
		tenants = append(tenants, tv)
	}
	live := 0
	for _, h := range hosts {
		if h.Live(time.Now()) {
			live++
		}
	}
	capv, resv := containerState(ctx, s.mgmt(), "")
	degraded := degradedStores(ctx, s.mgmt(), "")
	res := map[string]any{"linked": s.Link, "stateDir": s.Dir, "whoami": w, "hosts": hosts, "tenants": tenants, "healthy": live > 0,
		"hostCapacity": capv, "containerReservations": resv}
	if len(degraded) > 0 {
		res["degradedStores"] = degraded
	}
	emit(f, res, func() { printRemoteStatus(s, w, hosts, tenants, live, degraded, capv, resv) }) // status_view.go
	if live == 0 {
		return fail("unhealthy", "no live host at "+s.Link.Endpoint).withFix("tw status on the host")
	}
	return nil
}

// tierLine describes the isolation tiers the running host offers.
// tierLine says which tiers this host offers and how each isolates
// tenants: the process tier by the OS sandbox (Seatbelt on macOS,
// Landlock+seccomp on Linux; "none" is loud), the microVM tier by the
// Firecracker jailer + VM.
func tierLine(s *state) (line string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var hv hostd.StatusView
	_ = s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv)
	defer func() { line += containersUpNote(hv) }() // doctor_containers.go
	sb := hv.Sandbox
	switch {
	case sb == "":
		sb = "sandbox unknown"
	case sb == "none":
		sb = "NO PROCESS SANDBOX: see `tw doctor`"
	case strings.HasPrefix(sb, "landlock"):
		sb = "Landlock+seccomp, " + sb
	case sb == "seatbelt":
		sb = "Seatbelt, dev-grade isolation"
	}
	if runtime.GOOS == "darwin" {
		if slices.Contains(hv.Tiers, "microvm") {
			return "tiers: process [" + sb + "], microvm [Virtualization.framework, one VMM + VM service per guest, via " + hv.VMVia + "; \"tier\": \"microvm\" in tailworks.jsonc]"
		}
		return "tier: process [" + sb + "]"
	}
	if slices.Contains(hv.Tiers, "microvm") {
		return "tiers: process [" + sb + "], microvm [Firecracker, jailed, via " + hv.VMVia + "; \"tier\": \"microvm\" in tailworks.jsonc]"
	}
	return "tier: process [" + sb + "; microVM tier off: see `tw doctor`, or run `sudo ~/.tailworks/bin/tw host setup --microvm` once]"
}
