// tw-mgmt is tailworks' management plane (M2): tenants, deployments, the object
// registry with lineage, epoch-fenced namespace leases, the mint broker
// (OAuth client, or a Headscale API key), the per-tenant registration throttle, spawn checks,
// kill, the reaper and the hash-chained audit log.
//
// It serves the /v1 API (api/openapi.yaml) on its own tsnet node on the
// operator tailnet, authenticated by WhoIs; the tailworks CLI on the same machine
// reaches it through the node's loopback proxy (<state-dir>/loopback.json).
// Only with -local-dev (tw dev, hermetic tests) does it also serve a unix
// socket guarded by the per-host token.
//
// The daemon itself is internal/mgmtd; this binary parses the flags. The
// single-process `tw up` runs the same runtime embedded with the host agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/rosskukulinski/tailworks/internal/version"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/internal/mgmtd"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd"
)

func main() {
	if version.Asked(os.Args) {
		fmt.Println(version.String("tw-mgmt"))
		return
	}
	if handled, err := runMaintenance(os.Args[1:]); handled { // maintenance.go
		if err != nil {
			fmt.Fprintln(os.Stderr, "tw-mgmt:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tw-mgmt:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		stateDir    = flag.String("state-dir", filepath.Join(home, ".tailworks", "mgmt"), "state directory (0700): db, custody, tsnet state")
		localDev    = flag.Bool("local-dev", false, "ALSO serve the API on a unix socket guarded by the per-host token (tw dev, hermetic tests). Normal operation is tsnet only")
		socket      = flag.String("socket", filepath.Join(home, ".tailworks", "run", "mgmt.sock"), "-local-dev: the unix socket path")
		tokenFile   = flag.String("host-token-file", filepath.Join(home, ".tailworks", "hostd", "mgmt.token"), "-local-dev: per-host bearer token for the unix socket (0600; minted here, read by tw-hostd and the tw CLI; sent as Tw-Host-Token)")
		rotateToken = flag.Bool("rotate-host-token", false, "mint a fresh host token at start (tw up does this)")
		dbDSN       = flag.String("db", "", "store: SQLite path (default <state-dir>/mgmt.db) or postgres://… DSN")
		dbDSNFile   = flag.String("db-file", "", "read the store DSN from this file (keeps a Postgres password off argv)")
		tsapiURL    = flag.String("tsapi-url", tsapi.DefaultBaseURL, "Tailscale API base URL")
		tsHostname  = flag.String("tsnet-hostname", "", "serve the API on the operator tailnet as this tsnet node (e.g. tw-mgmt); required unless -local-dev")
		noLoopback  = flag.Bool("no-loopback", false, "don't publish the node's loopback proxy for the local tw CLI (<state-dir>/loopback.json)")
		versionFlag = flag.String("version", "", "deprecated: the version is built in (internal/version); a non-empty value overrides it for GET /v1/status")
		tsControl   = flag.String("tsnet-control-url", "", "operator tailnet control URL (required with -tsnet-hostname)")
		tsAuthFile  = flag.String("tsnet-authkey-file", "", "file with the operator-tailnet auth key for the first join (TS_AUTHKEY=…); without it the node state must already be logged in (tw up hands it over: -tsnet-authkey-once)")
		tsTags      = flag.String("tsnet-tags", mgmt.MgmtTag, "comma-separated tags the mgmt node advertises")
		tsAuthOnce  = flag.Bool("tsnet-authkey-once", false, "-tsnet-authkey-file is a single-use handoff (tw up): read and deleted before the join, a missing file is no key, and a failed start deletes it and records why in <state-dir>/start.err")
		tsService   = flag.String("tsnet-service", "", "also host this Tailscale Service (e.g. svc:tw-mgmt) on :80, advertised only while this replica's store answers: point every host at http://<label>.<tailnet> and run each replica with its own -tsnet-hostname (e.g. tw-mgmt-1). Policy needs autoApprovers.services for the mgmt tag and grants to the svc: name")
		tsTLS       = flag.Bool("tsnet-tls", true, "also serve the API on :443 with the node's HTTPS certificate when the tailnet has HTTPS enabled (plain :80 is always served)")
		admins      = flag.String("admins", "", "comma-separated tailnet logins admitted as admins even without the "+mgmt.AdminCap+" capability (override; the normal path is the cap, granted to "+mgmt.MgmtTag+")")
		custodyKind = flag.String("custody", "file", "custody backend: file (AES-GCM in <state-dir>/custody) or setec")
		setecURL    = flag.String("setec-url", "", "setec server URL (with -custody setec)")
		reapEvery   = flag.Duration("reap-interval", mgmtd.DefaultReapInterval, "reaper interval")
		delDelay    = flag.Duration("device-delete-delay", 0, "defer kill tier 2 (device delete) this long after the tier-1 push, so the host's tombstone answers new connections with object_killed (keep < hostd -kill-tombstone)")
		warmKeys    = flag.Int("warm-keys", mgmtd.DefaultWarmKeys, "warm-key pool: pre-minted single-use keys per (tenant, tag set), so a create skips the CreateKey call (sized from the recent create rate, capped here; 0 = off)")
		warmKeyTTL  = flag.Duration("warm-key-ttl", mgmt.DefaultWarmKeyTTL, "-warm-keys: a pooled key's expiry; unused keys are revoked shortly before it")
		warmRevoke  = flag.Bool("revoke-warm-keys-on-exit", false, "-warm-keys: on shutdown, revoke every unused pooled key at the API and delete it from custody (tw up sets it; leave it off for replicas that share a store, or each restart revokes the others' pool)")
		metricsPort = flag.Int("metrics-port", obs.DefaultMetricsPort, "port on mgmt's own tailnet node serving GET /metrics (Prometheus; who may scrape is a tailnet policy grant); 0 = off")
		verbose     = flag.Bool("v", false, "debug logging")
		registryDir = flag.String("registry-dir", "", "OCI registry blob store (ADR-032 D3; default <state-dir>/registry; \"off\" disables the registry). Replicas serving the registry must share it")
		regInsecure = flag.String("registry-insecure", "", "comma-separated upstream registry hosts mirrored over plain HTTP (tests: a local upstream)")
		noMirror    = flag.Bool("no-mirror", false, "refuse external image refs at deploy (only tw deploy builds and public bases); mgmt then makes no registry egress")
		bundleKeep  = flag.Int("bundle-retention", mgmt.DefaultBundleRetention, "bundles kept per workload for tw rollback (older deployments keep their record and hash, not the bundle); 0 = the default, -1 = keep every bundle")
		auditMaxAge = flag.Duration("audit-max-age", 0, "audit retention: delete audit records older than this (e.g. 2160h), keeping a checkpoint so the rest still verify; 0 = keep every record (the default)")
		auditMaxRec = flag.Int64("audit-max-records", 0, "audit retention: keep only the newest N records of each chain (with a checkpoint, as -audit-max-age); 0 = keep every record (the default)")
		aliasFile   = flag.String("image-aliases", "", "JSON file [{from, to, arch, source}] replacing the default image alias map (cloudflare/sandbox → tw.local arm64 rebuilds; source: a digest-pinned image that fills a missing target)")
	)
	// Management API limits for tailnet callers (mgmt/ratelimit.go).
	apiRPS := flag.Float64("api-rps", mgmt.DefaultAPIRPS, "per tailnet caller (WhoIs node; not hosts): sustained mgmt API requests per second, burst twice that; over it is 429 rate_limited (-1 = off)")
	apiHostRPS := flag.Float64("api-host-rps", mgmt.DefaultAPIHostRPS, "per host: sustained mgmt API requests per second, burst twice that (-1 = off)")
	apiInFlight := flag.Int("api-inflight", mgmt.DefaultAPIInFlight, "concurrent mgmt API requests from non-host tailnet callers, all together; over it is 429 rate_limited (-1 = off)")
	flag.Parse()
	lvl := slog.LevelInfo
	if *verbose {
		lvl = slog.LevelDebug
	}
	log := slog.New(netd.NewRedactingHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if v := os.Getenv("TW_TEST_WALL_CLOCK_OFFSET"); v != "" {
		// Test-only (mm-loop S8): simulate a wrong machine clock.
		d, err := time.ParseDuration(v)
		if err != nil || !*localDev {
			return fmt.Errorf("TW_TEST_WALL_CLOCK_OFFSET is a test hook: it needs -local-dev and a duration (got %q)", v)
		}
		mgmt.SetTestWallClockOffset(d)
		log.Warn("TEST HOOK: wall clock offset", "offset", d)
	}
	o := mgmtd.Options{StateDir: *stateDir, LocalDev: *localDev, Socket: *socket, HostTokenFile: *tokenFile, RotateHostToken: *rotateToken,
		DB: *dbDSN, DBFile: *dbDSNFile, TSAPIURL: *tsapiURL,
		TSNetHostname: *tsHostname, TSNetControlURL: *tsControl, TSNetAuthKeyFile: *tsAuthFile, TSNetService: *tsService, TSNetTLS: *tsTLS,
		NoLoopback: *noLoopback, Version: mgmtVersion(*versionFlag), Custody: *custodyKind, SetecURL: *setecURL,
		ReapInterval: *reapEvery, DeviceDeleteDelay: *delDelay, WarmKeys: *warmKeys, WarmKeyTTL: *warmKeyTTL,
		RegistryDir: *registryDir, NoMirror: *noMirror, ImageAliasesFile: *aliasFile, Log: log,
		MetricsPort: *metricsPort}
	o.BundleRetention = *bundleKeep
	o.AuditMaxAge, o.AuditMaxRecords = *auditMaxAge, *auditMaxRec // mgmt/retention.go
	o.APILimits = mgmt.APILimits{RPS: *apiRPS, HostRPS: *apiHostRPS, InFlight: *apiInFlight}
	o.RevokeWarmKeysOnClose = *warmRevoke
	o.TSNetAuthKeyOnce = *tsAuthOnce
	// Like the flags: "" means no tags at all (Options nil means the default).
	o.TSNetTags = []string{}
	if *tsTags != "" {
		o.TSNetTags = strings.Split(*tsTags, ",")
	}
	if *admins != "" {
		o.Admins = strings.Split(*admins, ",")
	}
	if *regInsecure != "" {
		o.RegistryInsecure = strings.Split(*regInsecure, ",")
	}
	rt, err := mgmtd.Start(ctx, o)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case err := <-rt.Errs():
		log.Error("listener failed", "err", err)
	}
	return rt.Close()
}

// mgmtVersion is the version mgmt reports: the built-in one, unless the
// deprecated -version flag overrides it (older `tw up` passes it).
func mgmtVersion(flagV string) string {
	if flagV != "" && version.IsDev(version.Version) {
		return flagV
	}
	return version.Version
}
