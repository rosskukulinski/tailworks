package hostd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/procsb"
	"github.com/rosskukulinski/tailworks/internal/wallclock"
)

// Process-tier sandboxes (Config.Sandbox).
const (
	SandboxNone = "none"
	// SandboxSeatbelt runs each workerd and Node app under macOS Seatbelt
	// (sandbox-exec). Every tailworks process runs as one uid on the process
	// tier, so 0700 dirs alone don't keep tenant A's workerd out of tenant
	// B's state: the profile denies every hostd data and run dir (node
	// keys, other tenants' DO storage and sockets, mgmt's admin socket,
	// API key files) except the instance's own dir and its own DO storage,
	// and (seatbelt_strict.go) writes, network, signals and exec outside
	// what the workload needs.
	SandboxSeatbelt = "seatbelt"
	// SandboxSeatbeltLegacy is the pre-#121 Seatbelt profile: files and unix
	// sockets only (TCP, signals and writes elsewhere allowed). Kept for one
	// release in case the strict profile breaks a workload.
	SandboxSeatbeltLegacy = "seatbelt-legacy"
	// SandboxLandlock runs each workerd and Node app on Linux under the
	// internal/procsb launcher: Landlock filesystem and TCP rules, no_new_privs
	// and a seccomp filter, with the same allow list as the Seatbelt profile
	// (its own dirs; read-only runtime binaries) plus TCP ports.
	SandboxLandlock = procsb.Name
	// SandboxAuto is seatbelt on macOS when sandbox-exec exists, landlock on
	// Linux when the kernel has Landlock, else none.
	SandboxAuto = "auto"
)

const sandboxExec = "/usr/bin/sandbox-exec"

// ResolveSandbox maps "auto"/"" to what this host supports.
func ResolveSandbox(s string) (string, error) {
	switch s {
	case "", SandboxNone:
		return SandboxNone, nil
	case SandboxAuto:
		if runtime.GOOS == "darwin" && isFileExec(sandboxExec) {
			return SandboxSeatbelt, nil
		}
		if runtime.GOOS == "linux" && procsb.Supported() {
			return SandboxLandlock, nil
		}
		return SandboxNone, nil
	case SandboxSeatbelt,
		SandboxSeatbeltLegacy:
		if runtime.GOOS != "darwin" || !isFileExec(sandboxExec) {
			return "", fmt.Errorf("sandbox %q needs macOS %s", s, sandboxExec)
		}
		return s, nil
	case SandboxLandlock:
		if runtime.GOOS != "linux" || !procsb.Supported() {
			_, err := procsb.ABI()
			return "", fmt.Errorf("sandbox %q needs Linux with Landlock enabled: %v", s, err)
		}
		return SandboxLandlock, nil
	}
	return "", fmt.Errorf("unknown sandbox %q (none | seatbelt | seatbelt-legacy | landlock | auto)", s)
}

func isFileExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// realPath resolves symlinks (/tmp → /private/tmp): Seatbelt matches
// resolved paths. Missing paths resolve their longest existing prefix.
func realPath(p string) string {
	p, _ = filepath.Abs(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if dir == "" || dir == p || dir == "/" {
		return p
	}
	return filepath.Join(realPath(strings.TrimSuffix(dir, "/")), base)
}

func sbQuote(p string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// SeatbeltProfile returns the profile workerd runs under with no TCP (see
// seatbeltProfile). tw doctor probes with it.
func SeatbeltProfile(own, readOnly, deny []string) string {
	return seatbeltProfile(own, readOnly, nil, deny, nil, nil)
}

// sandboxDeny is what no workerd may touch: the user's home dir (API key
// files, including those of tenants added after this workerd started, and
// everything else there), hostd's data and run dirs, the configured extra
// paths (tw up: its state and run dirs), and the API key file of every
// tenant this host serves.
func (h *Host) sandboxDeny() []string {
	deny := append([]string{h.cfg.DataDir, h.cfg.RunDir}, h.cfg.SandboxDeny...)
	if home, err := os.UserHomeDir(); err == nil && home != "/" && home != "" {
		deny = append(deny, home)
	}
	h.mu.Lock()
	for _, t := range h.tenants {
		if t.info.APIKeyFile != "" {
			deny = append(deny, t.info.APIKeyFile)
		}
		if t.info.OAuthClientFile != "" {
			deny = append(deny, t.info.OAuthClientFile)
		}
	}
	h.mu.Unlock()
	// Every tenant mgmt knows, including ones this host has no workload of
	// yet (best effort: the local socket allows it, a remote mgmt may not).
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	// Every tenant's credential files, including tenants with nothing on
	// this host yet (paths only; GET /v1/tenants shows a host only its own
	// tenants). An older mgmt without the route: the tenant list, as before.
	if paths, err := h.mgmt.SandboxDenyPaths(ctx, h.cfg.HostID); err == nil {
		deny = append(deny, paths...)
	} else if ts, err := h.mgmt.Tenants(ctx); err == nil {
		for _, t := range ts {
			if t.APIKeyFile != "" {
				deny = append(deny, t.APIKeyFile)
			}
			if t.OAuthClientFile != "" {
				deny = append(deny, t.OAuthClientFile)
			}
		}
	}
	return deny
}

// workerdCommand builds the instance's workerd command line, wrapped in its
// tenant's sandbox. workerd needs no TCP at all: its listener and every
// service it calls are unix sockets in its own instance dir.
//
// Under Landlock it gets no unix sockets: its ingress listener is passed as
// fd 3 (--socket-fd) and its outbound services are loopback TCP ports (the
// only ones it may connect to); the profile denies socket(AF_UNIX).
func (inst *instance) workerdCommand() (*exec.Cmd, error) {
	args := []string{"serve", inst.res.Config, "--experimental"}
	var f *os.File
	if inst.ingressLn != nil {
		var err error
		if f, err = inst.ingressLn.File(); err != nil {
			return nil, err
		}
		args = append(args, "--socket-fd", "ingress=3")
	}
	args, bind := inst.inspectorArgs(args) // inspector.go: tw dev --inspector-port
	args = append(args, inst.pyodideArgs()...)
	var trees []string
	if inst.pyodide != "" {
		trees = []string{inst.pyodide} // pyodide.go: read-only
	}
	_, clockFiles := wallclock.Workerd() // a test build's skewed clock; nil otherwise
	cmd, err := inst.sandboxed(inst.h.cfg.WorkerdBin, args,
		[]string{inst.dir, inst.doStorage}, append([]string{inst.h.cfg.WorkerdBin}, clockFiles...), trees, bind, inst.loopPorts)
	if err != nil {
		if f != nil {
			f.Close()
		}
		return nil, err
	}
	cmd.Env = WorkerdEnv() // workerdenv.go (#336): not hostd's environment, on any branch
	if f != nil {
		cmd.ExtraFiles = []*os.File{f}
	}
	return cmd, nil
}

// nodeCommand builds the Node tier's command line under the same sandbox as
// workerd: tenant code too. It may write only its instance dir and its own
// data dir; it may read the node binary and, when node lives in a
// "<prefix>/bin" dir (nvm, Homebrew, a tarball), that install prefix.
//
// On Linux it may also bind and connect only its own app port (TCP listen
// mode) and connect only to its egress proxy's port: every other TCP
// destination, loopback included, is refused by Landlock, so the app can't
// bypass HTTP_PROXY.
func (inst *instance) nodeCommand(args []string) (*exec.Cmd, error) {
	np := inst.node
	var bind, connect []uint16
	if np.port != 0 {
		bind, connect = []uint16{uint16(np.port)}, []uint16{uint16(np.port)}
	}
	if np.confined() { // nodelisten.go: hostd owns the listener
		bind = nil
	}
	if a, ok := np.proxy.Addr().(*net.TCPAddr); ok {
		connect = append(connect, uint16(a.Port))
	}
	return inst.sandboxed(np.bin, args, []string{inst.dir, np.dataDir}, []string{np.bin}, nodePrefix(np.bin), bind, connect)
}

// nodePrefix is node's install prefix (…/bin/node → …), if it has one.
func nodePrefix(bin string) []string {
	r := realPath(bin)
	if d := filepath.Dir(r); filepath.Base(d) == "bin" {
		if p := filepath.Dir(d); p != "/" && p != "/usr" && p != "." {
			return []string{p}
		}
	}
	return nil
}

// sandboxed returns bin args, run under the host's sandbox (Seatbelt:
// sandbox-exec execs bin in place; Landlock: the procsb launcher does, so
// the pid is bin's either way). The profile is written to the instance dir
// and recorded as inst.profile. bindTCP and connectTCP are the only TCP
// ports allowed (Landlock: by port; Seatbelt: on localhost).
func (inst *instance) sandboxed(bin string, args, own, readOnly, readOnlyTrees []string, bindTCP, connectTCP []uint16) (*exec.Cmd, error) {
	if inst.h.sandbox == SandboxLandlock {
		launcher, err := inst.h.sandboxLauncher()
		if err != nil {
			return nil, err
		}
		// No AF_UNIX socket(): workerd's listener is inherited and its
		// services are loopback TCP; a Node app listens on TCP. Only a Node
		// app in listen "unix" mode must create its own unix socket.
		noUnix := inst.ingressLn != nil || inst.node != nil && inst.node.listen != "unix"
		prof := &procsb.Profile{Launcher: launcher, Exec: readOnly, ReadOnly: readOnlyTrees, Own: own,
			BindTCP: bindTCP, ConnectTCP: connectTCP, NoUnixSockets: noUnix, Deny: inst.h.sandboxDeny()}
		prof.NoListen = inst.node != nil && inst.node.confined() // nodelisten.go
		p := filepath.Join(inst.dir, "sandbox.json")
		if err := prof.Write(p); err != nil {
			return nil, err
		}
		inst.mu.Lock()
		inst.profile = p
		inst.mu.Unlock()
		return procsb.Command(launcher, p, bin, args...), nil
	}
	var prof, p string
	switch inst.h.sandbox {
	case SandboxSeatbelt:
		prof = seatbeltProfile(own, readOnly, readOnlyTrees, inst.h.sandboxDeny(), bindTCP, connectTCP)
		p = filepath.Join(inst.dir, "sandbox.sb")
	case SandboxSeatbeltLegacy:
		prof = seatbeltLegacyProfile(own, readOnly, readOnlyTrees, inst.h.sandboxDeny())
		p = filepath.Join(inst.dir, legacyProfileName)
	default:
		return exec.Command(bin, args...), nil
	}
	if err := os.WriteFile(p, []byte(prof), 0o600); err != nil {
		return nil, err
	}
	inst.mu.Lock()
	inst.profile = p
	inst.mu.Unlock()
	return exec.Command(sandboxExec, append([]string{"-f", p, bin}, args...)...), nil
}

// sandboxLauncher is the binary that applies Landlock profiles: any binary
// importing internal/procsb (tw-hostd; in tests, the test binary).
func (h *Host) sandboxLauncher() (string, error) {
	if h.cfg.SandboxLauncher != "" {
		return h.cfg.SandboxLauncher, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("sandbox launcher: %w", err)
	}
	return exe, nil
}

// sandboxOf names the sandbox a process-tier instance runs under, from its
// profile ("" = none).
func sandboxOf(profile string) string {
	switch filepath.Ext(profile) {
	case ".sb":
		if filepath.Base(profile) == legacyProfileName {
			return SandboxSeatbeltLegacy
		}
		return SandboxSeatbelt
	case ".json":
		return SandboxLandlock
	}
	return SandboxNone
}

// SandboxCommand runs args[0] args[1:] under a workload's recorded sandbox
// profile (WorkloadStatus.SandboxProfile): sandbox-exec on macOS, the
// procsb launcher on Linux. Tests use it to check what a workload can
// reach.
func SandboxCommand(profile string, args ...string) (*exec.Cmd, error) {
	switch sandboxOf(profile) {
	case SandboxSeatbelt,
		SandboxSeatbeltLegacy:
		return exec.Command(sandboxExec, append([]string{"-f", profile}, args...)...), nil
	case SandboxLandlock:
		p, err := procsb.Load(profile)
		if err != nil {
			return nil, err
		}
		return procsb.Command(p.Launcher, profile, args[0], args[1:]...), nil
	}
	return nil, fmt.Errorf("no sandbox profile %q", profile)
}

// landlockTCPABI is the first Landlock ABI with TCP connect/bind rules.
const landlockTCPABI = 4

// landlockABI is the kernel's Landlock ABI when sandbox is Landlock, else 0.
func landlockABI(sandbox string) int {
	if sandbox != SandboxLandlock {
		return 0
	}
	abi, _ := procsb.ABI()
	return abi
}

// egressEnforcement is EgressEnforcement for a given Landlock ABI.
func egressEnforcement(sandbox string, abi int) string {
	if sandbox == SandboxLandlock && abi >= landlockTCPABI {
		return EgressEnforced
	}
	if sandbox == SandboxSeatbelt { // TCP connects only to localhost:<proxy>, <own port>
		return EgressEnforced
	}
	return EgressUnenforced
}

// SandboxInfo describes this host's process sandbox for logs and status:
// "seatbelt", "landlock (ABI n)" or "none".
func (h *Host) SandboxInfo() string {
	if h.sandbox == SandboxLandlock {
		abi, _ := procsb.ABI()
		return fmt.Sprintf("landlock (ABI %d)", abi)
	}
	return h.sandbox
}
