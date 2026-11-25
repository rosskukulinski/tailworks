package main

import (
	"bytes"
	"context"
	"fmt"
	twversion "github.com/rosskukulinski/tailworks/internal/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// version is the release version (internal/version, set at build time).
var version = twversion.Version

// workerdPin is the workerd npm version tailworks is built and tested against
// (internal/version.WorkerdPin, checked against package-lock.json);
// `workerd --version` prints its date.
var (
	workerdPin     = twversion.WorkerdPin
	workerdPinDate = twversion.WorkerdPinDate()
)

func cmdVersion(args []string) error {
	f := newFlags("version")
	check := f.Bool("check", false, "compare with the latest release, print how to upgrade, and say whether the running tw up needs a restart")
	if _, err := f.parse(args); err != nil {
		return err
	}
	if *check {
		return cmdVersionCheck(f) // updatecheck.go
	}
	v := map[string]any{"tw": version, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH,
		"workerdPin": workerdPin, "esbuild": esbundle.Version,
		"updateCheck": updateCheckOn(), // updatecheck.go
	}
	emit(f, v, func() {
		fmt.Printf("tw %s (%s, %s) · workerd %s · esbuild %s (built in)\n", version, v["os"], v["go"], workerdPin, esbundle.Version)
	})
	return nil
}

type check struct {
	Name   string  `json:"name"`
	Status string  `json:"status"` // ok | warn | fail | skip
	Detail string  `json:"detail"`
	Fix    string  `json:"fix,omitempty"`
	MS     float64 `json:"ms"`

	// Group is the section the summary output lists the check under
	// (doctor_render.go).
	Group string `json:"group"`
}

// cmdDoctor checks this machine can run tailworks: workerd runs, the bundler and
// templates are there (offline), ~/.tailworks and the state dir are writable, the
// mint credential is usable, and tailworks is not using the system tailscaled.
func cmdDoctor(args []string) error {
	f := newFlags("doctor")
	verbose := f.Bool("v", false, "print every check with its full detail (default: problems, then one line per group)")
	if _, err := f.parse(args); err != nil {
		return err
	}
	var checks []check
	run := func(name string, fn func() (status, detail, fix string)) {
		t := time.Now()
		st, d, fx := fn()
		checks = append(checks, check{Name: name, Status: st, Detail: d, Fix: fx, MS: ms(time.Since(t)), Group: metaOf(name).group})
	}
	s, serr := openState(*f.stateDir)

	run("binaries", func() (string, string, string) {
		var miss []string
		for _, b := range []string{"tw-mgmt", "tw-hostd"} {
			if _, err := findBin(b); err != nil {
				miss = append(miss, b)
			}
		}
		if len(miss) > 0 {
			if serr == nil && s.configured() && s.Cfg.single() {
				// Single-process tw up runs both daemons inside tailworks itself;
				// the binaries matter only for --separate-host (and
				// tw-hostd on a machine added with tw host add).
				return "info", "not found next to tailworks (" + exeDir() + ") or on PATH: " + strings.Join(miss, ", ") + "; not needed in single-process mode (the daemons run inside tailworks), only for tw up --separate-host", "reinstall tailworks (install.sh) if you want the two-process layout"
			}
			return "fail", "not found next to tailworks (" + exeDir() + ") or on PATH: " + strings.Join(miss, ", "), "reinstall tailworks (install.sh)"
		}
		return "ok", "tw " + version + ", tw-mgmt and tw-hostd in " + exeDir(), ""
	})

	doctorPlatform(run) // Linux: glibc (before workerd, which needs it)

	run("workerd", func() (string, string, string) {
		wd, err := findWorkerd("")
		if err != nil {
			return "fail", "workerd not found", "reinstall tw, or set $TW_WORKERD"
		}
		v, fix, err := workerdRuns(wd, "see the glibc check above") // up_preflight.go
		if err != nil {
			return "fail", err.Error(), fix
		}
		if runtime.GOOS == "darwin" {
			if q, _ := exec.Command("xattr", "-p", "com.apple.quarantine", wd).Output(); len(bytes.TrimSpace(q)) > 0 {
				return "warn", wd + " runs (" + v + ") but is quarantined", "xattr -d com.apple.quarantine " + wd
			}
		}
		if !strings.Contains(v, workerdPinDate) {
			return "warn", wd + ": " + v + " (tailworks is tested with " + workerdPin + ")", ""
		}
		return "ok", wd + ": " + v + " (pinned " + workerdPin + ")", ""
	})

	run("workerd_age", func() (string, string, string) { return workerdAgeCheck(workerdPin, time.Now()) }) // doctor_workerdage.go

	run("bundler", func() (string, string, string) {
		ct, err := createTailworksDir()
		if err != nil {
			return "fail", "templates (create-tailworks) not found", reinstallFix("reinstall tw") // sourcehints.go
		}
		np := nodePaths(ct)
		tmp, err := os.MkdirTemp("", "tw-doctor-")
		if err != nil {
			return "fail", err.Error(), ""
		}
		defer os.RemoveAll(tmp)
		var sizes []string
		ents, _ := os.ReadDir(filepath.Join(ct, "templates"))
		var tms []string
		for _, e := range ents {
			if e.IsDir() {
				tms = append(tms, e.Name())
			}
		}
		if len(tms) == 0 {
			return "fail", "no templates in " + ct, reinstallFix("reinstall tw")
		}
		var nodeTms, pyTms []string
		for _, tm := range tms {
			dir := filepath.Join(ct, "templates", tm)
			if isNodeRuntimeDir(dir) {
				// runtime "node" (M9) templates are bundled by build-node.mjs at
				// deploy time and need Node.js; they are not workerd bundles.
				nodeTms = append(nodeTms, tm)
				continue
			}
			wb, err := os.ReadFile(filepath.Join(dir, "wrangler.jsonc"))
			if err != nil {
				return "fail", "template " + tm + " missing in " + ct, reinstallFix("reinstall tw")
			}
			var w struct {
				Main string `json:"main"`
			}
			bundle.UnmarshalJSONC(wb, &w)
			if bundle.IsPythonMain(w.Main) {
				pyTms = append(pyTms, tm) // python.go: packed as it is, no bundler
				continue
			}
			n, err := esbundle.Build(esbundle.Options{Dir: dir, Main: w.Main, Outfile: filepath.Join(tmp, tm+".js"), NodePaths: np})
			if err != nil {
				return "fail", "bundling template " + tm + " failed: " + firstLine(err.Error()) + noNodeModules(np, ct), reinstallFix("reinstall tailworks (the vendored node_modules may be incomplete)")
			}
			sizes = append(sizes, fmt.Sprintf("%s %.0f KB", tm, float64(n)/1024))
		}
		detail := "esbuild " + esbundle.Version + " (built in, no Node.js needed) bundles every workerd template offline: " + strings.Join(sizes, ", ")
		if len(nodeTms) > 0 {
			detail += "; Node-tier templates (need Node.js to deploy): " + strings.Join(nodeTms, ", ")
		}
		if len(pyTms) > 0 {
			detail += "; Python templates (no bundling): " + strings.Join(pyTms, ", ")
		}
		return "ok", detail, ""
	})

	run("install", doctorInstall) // npm_install.go: tarball, npm or npx

	run("tw_home_writable", func() (string, string, string) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "fail", err.Error(), ""
		}
		return writable(filepath.Join(home, ".tailworks"))
	})

	run("state_dir_writable", func() (string, string, string) {
		if serr != nil {
			return "fail", serr.Error(), ""
		}
		return writable(s.Dir)
	})

	run("mint_mode", func() (string, string, string) {
		switch {
		case s != nil && s.remote():
			return "info", "this machine is linked to " + s.Link.Endpoint + " (tw link: your Tailscale identity, no key or token here)", ""
		case s == nil || !s.configured():
			return "skip", "tw up not run here (tw up --oauth-client-file <file>: mint mode oauth)", ""
		case s.Cfg.oauth():
			d := "oauth: OAuth client " + s.Cfg.OAuthClientID + " from " + s.Cfg.OAuthClientFile + " mints every node key"
			st, err := os.Stat(s.Cfg.OAuthClientFile)
			switch {
			case err != nil:
				return "fail", d + "; " + err.Error(), "restore the file (TS_OAUTH_CLIENT_ID=…, TS_OAUTH_CLIENT_SECRET=tskey-client-…, mode 0600)"
			case st.Mode().Perm()&0o077 != 0:
				return "warn", fmt.Sprintf("%s; the file has mode %#o: other users can read the secret, and tw-mgmt refuses it", d, st.Mode().Perm()), "chmod 600 " + s.Cfg.OAuthClientFile
			}
			return "ok", d, ""
		}
		if s.Cfg.headscale() {
			return "ok", "headscale (ADR-015): the Headscale API key in " + s.Cfg.APIKeyFile + " mints every node key at " + s.Cfg.ControlURL + " (a long-lived secret: rotate it; no Services, no Funnel, no posture)", ""
		}
		return "fail", "mint mode " + s.Cfg.modeName() + " was removed: this state can't mint until it is switched to an OAuth client", "tw up --oauth-client-file <file>   (keeps the tenant and its deployments; docs/INSTALL.md)"
	})

	run("transport", func() (string, string, string) {
		switch {
		case s == nil:
			return "skip", "no state", ""
		case s.remote():
			return "ok", "tsnet: " + s.transport() + " → " + s.Link.Endpoint + " (WhoIs identity; no key or token here)", ""
		case !s.configured():
			return "skip", "tw up not run here (normal operation: tsnet only)", ""
		case s.Cfg.LocalDev:
			return "warn", "local-dev: mgmt serves a unix socket with the per-host token and hostd uses it (tw up --local-dev; development only, not the product transport)", "tw down --purge, remove " + s.Dir + ", tw up (without --local-dev)"
		}
		d := "tsnet: mgmt API on its own node " + s.Cfg.MgmtDNSName + " (WhoIs); hostd on its own tag:tw-host node " + mgmt.HostNodeName(s.Cfg.HostID) + "; this CLI dials through mgmt's loopback proxy"
		if s.Cfg.single() {
			d = "tsnet: mgmt API on its own node " + s.Cfg.MgmtDNSName + " (WhoIs); host embedded in the mgmt process (single process, one infrastructure device; in-process transport and pushes); this CLI dials through mgmt's loopback proxy"
		}
		if isFile(s.mgmtSock()) {
			return "fail", d + "; but a mgmt unix socket exists at " + s.mgmtSock(), "a stale or foreign tw-mgmt -local-dev is running; stop it"
		}
		if s.supervisorAlive() && s.loopback() == nil {
			return "warn", d + "; mgmt's loopback descriptor is missing (mgmt starting?)", "tw status"
		}
		return "ok", d, ""
	})

	run("api_key", func() (string, string, string) {
		if s == nil || !s.Cfg.headscale() {
			return "skip", "no API key on this machine (a Tailscale tailnet mints through its OAuth client; only the Headscale profile uses an API key)", ""
		}
		kf, _ := filepath.Abs(s.Cfg.APIKeyFile)
		return doctorHeadscaleKey(s, kf)
	})

	doctorPublic(run, s)
	doctorStorage(run, s) // doctor_storage.go: data_disk, store_sizes
	// nodebudget.go: each tenant's live tailnet nodes.
	run("node_budget", func() (string, string, string) { return doctorNodeBudget(s) })

	run("sandbox", func() (string, string, string) { return doctorSandbox(s) })
	run("host_sockets", func() (string, string, string) { return doctorHostSockets(s) })
	run("side_channels", func() (string, string, string) { return doctorSideChannels(s) })
	run("worker_internet", func() (string, string, string) { return doctorWorkerInternet(s) }) // up_workerinternet.go
	run("containers", func() (string, string, string) { return doctorContainers(s) })

	doctorLinuxHost(run, s) // Linux: kernel, Landlock, user namespaces, KVM, firecracker, cgroups v2, systemd, microvm_tier

	run("node_egress", func() (string, string, string) {
		// RT-08: the Node tier's egress proxy is a boundary only under a
		// network-confining sandbox (Landlock ABI ≥ 4) or a microVM.
		if s == nil || !s.configured() || !s.supervisorAlive() {
			return "skip", "tw is not running here", ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var hv hostd.StatusView
		if err := s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv); err != nil {
			return "skip", "hostd status unavailable: " + firstLine(err.Error()), ""
		}
		var unenforced []string
		tenants := map[string]bool{}
		for _, w := range hv.Workloads {
			tenants[w.TenantID] = true
			if w.Runtime == "node" && w.Egress != hostd.EgressEnforced {
				unenforced = append(unenforced, w.Workload)
			}
		}
		if len(unenforced) == 0 {
			return "ok", "no Node-runtime workload with unenforced egress (" + runtime.GOOS + ": workerd apps egress only via netd)", ""
		}
		d := fmt.Sprintf("Node-tier egress is UNENFORCED for %s: the HTTP(S) proxy is cooperative; a hostile app can open raw sockets to loopback, LAN, metadata and the internet (RT-08, docs/node-tier.md)", strings.Join(unenforced, ", "))
		if len(tenants) > 1 {
			d += fmt.Sprintf("; this host serves %d tenants", len(tenants))
			if hv.InsecureSharedHost {
				d += " with --insecure-shared-host"
			}
		}
		return "warn", d, "run Node apps on Linux with Landlock (ABI ≥ 4) or in a microVM; keep them off multi-tenant hosts"
	})

	run("system_tailscaled_not_used", func() (string, string, string) {
		var notes []string
		if s != nil && s.configured() {
			if s.Cfg.ControlURL == "" {
				return "fail", "tw.json has no explicit control URL", "tw down, then tw up --tailnet <tailnet> …"
			}
			notes = append(notes, "tw nodes are tsnet nodes of "+s.Cfg.Tailnet+" (control "+s.Cfg.ControlURL+", state under "+filepath.Join(s.Dir, "hostd")+")")
			if s.Cfg.MgmtDNSName != "" {
				notes = append(notes, "mgmt's node "+s.Cfg.MgmtDNSName+" is a tsnet node too (state under "+s.mgmtTsnetDir()+"), joined by its own login")
			}
		} else {
			notes = append(notes, "tw only ever runs tsnet nodes for the tailnet you name (no bootstrap from tailscaled)")
		}
		// Running tailworks processes must not hold the system tailscaled's
		// LocalAPI socket or state (lsof on macOS, /proc + ss on Linux;
		// never contacts tailscaled).
		if s != nil && s.supervisorAlive() {
			pids := append([]int{s.supervisorPID()}, childPIDs(s.supervisorPID())...)
			if bad, method := tailscaledHolders(pids); method != "" {
				if len(bad) > 0 {
					return "fail", "a tailworks process uses the system tailscaled: " + strings.Join(bad, "; "), "report this: tailworks must never use the system tailscaled"
				}
				notes = append(notes, fmt.Sprintf("%d running tw processes hold no tailscaled LocalAPI socket (%s)", len(pids), method))
			}
		}
		if systemTailscaled() {
			notes = append(notes, "a system Tailscale is installed on this machine and is left alone (its state is not read)")
		}
		return "ok", strings.Join(notes, "; "), ""
	})

	failed := false
	for _, c := range checks {
		if c.Status == "fail" {
			failed = true
		}
	}
	res := map[string]any{"ok": !failed, "version": version, "checks": checks}
	emit(f, res, func() {
		doctorHeader(os.Stdout, runtime.GOOS, runtime.GOARCH) // doctor_render.go
		if *verbose {
			renderDoctorVerbose(os.Stdout, checks)
			return
		}
		home, _ := os.UserHomeDir()
		renderDoctorSummary(os.Stdout, checks, home, doctorWidth())
	})
	if failed {
		return fail("doctor_failed", "one or more checks failed (see above; tw doctor -v for every check)")
	}
	return nil
}

// writable checks dir (or, if it doesn't exist yet, the directory that
// would hold it) accepts new files. It creates nothing that stays.
func writable(dir string) (string, string, string) {
	probe, note := dir, ""
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		probe, note = filepath.Dir(dir), " (not created yet; "+filepath.Dir(dir)+" is writable)"
	}
	fp, err := os.CreateTemp(probe, ".tailworks-doctor-*")
	if err != nil {
		return "fail", probe + " is not writable: " + err.Error(), "chmod u+w " + probe
	}
	fp.Close()
	os.Remove(fp.Name())
	if note != "" {
		return "ok", dir + note, ""
	}
	return "ok", dir + " is writable", ""
}

func childPIDs(pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var r []int
	for _, f := range strings.Fields(string(out)) {
		if p, err := strconv.Atoi(f); err == nil {
			r = append(r, p)
			r = append(r, childPIDs(p)...)
		}
	}
	return r
}

func lookPathOK(name string) bool { _, err := exec.LookPath(name); return err == nil }

func firstLine(s string) string { l, _, _ := strings.Cut(strings.TrimSpace(s), "\n"); return l }

// isNodeRuntimeDir reports whether dir's tailworks.jsonc declares runtime "node".
func isNodeRuntimeDir(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "tailworks.jsonc"))
	if err != nil {
		return false
	}
	var c struct {
		Runtime string `json:"runtime"`
	}
	if bundle.UnmarshalJSONC(b, &c) != nil {
		return false
	}
	return c.Runtime == "node"
}
